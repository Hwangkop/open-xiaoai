package speechhost

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"net"
	"os"
	"sync"
	"time"
)

// Host 持有 speech.usock，扮演原生 mico_aivs_lab 的角色。
//
// 它只做三件事：回应注册、把唤醒事件告诉上层、把 ASR 音频交出去。
// 什么时候结束一次收音由上层（服务端 VAD）决定，调用 StopTurn。
type Host struct {
	conn    *net.UnixConn
	onWake  func(activate uint64)
	onAudio func([]byte)

	mu         sync.Mutex
	peer       *net.UnixAddr
	dialogID   string
	closed     bool
	turnTimer  *time.Timer
	turnPeak   int32
	turnFrames int
}

// Start 绑定语音 socket 并开始收包。socketPath 上如果已有文件，会先删掉
// （调用方负责把原生 socket 备份走）。
func Start(socketPath string, onWake func(uint64), onAudio func([]byte)) (*Host, error) {
	if err := os.Remove(socketPath); err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("清理旧 socket 失败: %w", err)
	}
	conn, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: socketPath, Net: "unixgram"})
	if err != nil {
		return nil, fmt.Errorf("绑定 %s 失败: %w", socketPath, err)
	}
	h := &Host{conn: conn, onWake: onWake, onAudio: onAudio}
	go h.loop()
	log.Printf("🎙️ 语音宿主已监听 %s", socketPath)
	return h, nil
}

func (h *Host) loop() {
	buf := make([]byte, 256*1024)
	for {
		n, addr, err := h.conn.ReadFromUnix(buf)
		if err != nil {
			h.mu.Lock()
			closed := h.closed
			h.mu.Unlock()
			if closed {
				return
			}
			time.Sleep(50 * time.Millisecond)
			continue
		}
		msg, err := parseUpward(buf[:n])
		if err != nil {
			continue
		}
		h.mu.Lock()
		if addr != nil {
			h.peer = addr
		}
		h.mu.Unlock()

		switch msg.Type {
		case upRegister:
			log.Printf("🎙️ mips 注册: vendor=%q codec=%q", msg.Vendor, msg.Codec)
			h.send(downward(downRegisterResponse, "", 0, nil))
			h.send(downward(downEnableWakeup, "", 0, nil))
		case upStreamPrepare:
			id := newDialogID()
			h.mu.Lock()
			h.dialogID = id
			h.mu.Unlock()
			resp := fieldVarint(1, 1) // {ok: true}
			h.send(downward(downPrepareResponse, id, 3, resp))
			log.Printf("🎙️ 原生收音开始 dialog=%s activate=%d", id, msg.Activate)
			// 一轮收音最长 20 秒：mips 在 dialog 打开期间会一直推静音，
			// 没有这个兜底，服务端 VAD 不触发时 dialog 会一直挂着。
			h.mu.Lock()
			if h.turnTimer != nil {
				h.turnTimer.Stop()
			}
			h.turnTimer = time.AfterFunc(20*time.Second, h.StopTurn)
			h.mu.Unlock()
			h.mu.Lock()
			h.turnPeak = 0
			h.turnFrames = 0
			h.mu.Unlock()
			if h.onWake != nil {
				h.onWake(msg.Activate)
			}
		case upStreamTransmit:
			if msg.StreamType == streamASR && len(msg.Data) > 0 {
				// 统计本轮峰值，方便判断"到底有没有收到人声"
				var peak int32
				for i := 0; i+1 < len(msg.Data); i += 2 {
					v := int32(int16(uint16(msg.Data[i]) | uint16(msg.Data[i+1])<<8))
					if v < 0 {
						v = -v
					}
					if v > peak {
						peak = v
					}
				}
				h.mu.Lock()
				h.turnFrames++
				if peak > h.turnPeak {
					h.turnPeak = peak
				}
				h.mu.Unlock()
				if h.onAudio != nil {
					h.onAudio(msg.Data)
				}
			}
		case upStreamEnd:
			log.Printf("🎙️ 原生收音结束 dialog=%s", h.dialogID)
		case upStreamCancel:
			log.Printf("🎙️ 原生收音取消 dialog=%s", h.dialogID)
			h.clearDialog()
		}
	}
}

// StopTurn 结束本轮收音：停采集 + 结束 dialog（不续听）。
func (h *Host) StopTurn() {
	h.mu.Lock()
	id := h.dialogID
	h.dialogID = ""
	if h.turnTimer != nil {
		h.turnTimer.Stop()
		h.turnTimer = nil
	}
	h.mu.Unlock()
	if id == "" {
		return
	}
	h.send(downward(downStopCapture, id, 0, nil))
	finish := fieldVarint(1, 0) // 1: false
	finish = append(finish, fieldVarint(2, 0)...)
	finish = append(finish, fieldVarint(3, 0)...)
	finish = append(finish, fieldVarint(4, 1)...)
	finish = append(finish, fieldVarint(5, 1)...)
	finish = append(finish, fieldVarint(6, 1)...)
	h.send(downward(downDialogFinish, id, 6, finish))
	h.mu.Lock()
	peak, frames := h.turnPeak, h.turnFrames
	h.mu.Unlock()
	log.Printf("🎙️ 已结束本轮收音 dialog=%s（%d 帧，峰值 %d）", id, frames, peak)
}

func (h *Host) clearDialog() {
	h.mu.Lock()
	h.dialogID = ""
	h.mu.Unlock()
}

func (h *Host) send(frame []byte) {
	h.mu.Lock()
	peer := h.peer
	h.mu.Unlock()
	if peer == nil {
		return
	}
	if _, err := h.conn.WriteToUnix(frame, peer); err != nil {
		log.Printf("⚠️ 语音宿主发送失败: %v", err)
	}
}

// Close 停止监听。
func (h *Host) Close() {
	h.mu.Lock()
	h.closed = true
	h.mu.Unlock()
	_ = h.conn.Close()
}

func newDialogID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}
