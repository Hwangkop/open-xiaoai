package audio

// 麦克风持续推流（服务端唤醒模式）。
//
// 小爱固件用 ALSA dsnoop 共享采集（/etc/asound.conf 里 pcm.noop → pcm.Capture），
// 所以可以再起一个 arecord 进程与固件同时收音，互不影响。
//
// 关键坑：A113 的 PDM 数据放在 S32_LE 的**低 24 位**里。
// 直接让 ALSA 的 plug 插件转成 S16 只会取高 16 位，结果几乎全是 0（实测 RMS≈8）。
// 必须自己抓 S32_LE / 4ch / 48kHz，然后：
//   1. 四通道混成单声道
//   2. 右移 8 位（24bit → 16bit）
//   3. 48kHz 降到 16kHz（每 3 个样点取均值）

import (
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"os/exec"
	"sync"
)

const (
	micOutRate    = 16000
	micOutFrame   = 640 // 20ms @16kHz mono S16_LE
	micInRate     = 48000
	micInChannels = 4
	micDecimate   = micInRate / micOutRate // 3
	micInFrame    = micInChannels * 4      // 16 bytes：4ch × S32
)

// MicStreamer 用 arecord 持续收音并转成 16k/mono/S16_LE。
type MicStreamer struct {
	cmd    *exec.Cmd
	stopCh chan struct{}
	once   sync.Once
	done   chan struct{}
}

// StartMicStream 启动采集，每 20ms（640 字节）回调一次 onChunk。
// device 留空用 "noop"（固件定义的共享设备），也可传 "Capture"。
func StartMicStream(device string, onChunk func([]byte) error) (*MicStreamer, error) {
	if device == "" {
		device = "noop"
	}
	if _, err := exec.LookPath("arecord"); err != nil {
		return nil, fmt.Errorf("找不到 arecord: %w", err)
	}

	cmd := exec.Command("arecord",
		"-q",
		"-D", device,
		"-f", "S32_LE",
		"-r", fmt.Sprintf("%d", micInRate),
		"-c", fmt.Sprintf("%d", micInChannels),
		"-t", "raw",
		"-",
	)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("启动 arecord 失败: %w", err)
	}

	m := &MicStreamer{cmd: cmd, stopCh: make(chan struct{}), done: make(chan struct{})}
	log.Printf("🎤 麦克风推流已启动: arecord -D %s %dHz %dch S32 → %dHz mono S16",
		device, micInRate, micInChannels, micOutRate)

	go func() {
		defer close(m.done)
		convertLoop(stdout, onChunk, m.stopCh)
		_ = cmd.Wait()
		log.Printf("🎤 麦克风推流已停止")
	}()

	return m, nil
}

// convertLoop 读取 S32/4ch/48k，输出 16k/mono/S16 的 20ms 帧。
func convertLoop(r io.Reader, onChunk func([]byte) error, stop <-chan struct{}) {
	inBuf := make([]byte, micInFrame*micDecimate*64) // 一批约 64 个输出帧
	out := make([]byte, 0, micOutFrame*2)
	carry := make([]byte, 0, micInFrame)

	for {
		select {
		case <-stop:
			return
		default:
		}

		n, err := r.Read(inBuf)
		if n > 0 {
			// 可能读到不完整的帧，余数要留到下一次，否则会丢字节导致通道错位
			carry = append(carry, inBuf[:n]...)
			usable := len(carry) - len(carry)%micInFrame
			if usable > 0 {
				out = append(out, convertChunk(carry[:usable])...)
				carry = append(carry[:0], carry[usable:]...)
			}
			// 攒够 640 字节就发一帧
			for len(out) >= micOutFrame {
				chunk := make([]byte, micOutFrame)
				copy(chunk, out[:micOutFrame])
				out = out[micOutFrame:]
				if cbErr := onChunk(chunk); cbErr != nil {
					log.Printf("⚠️ 推流失败: %v", cbErr)
				}
			}
		}
		if err != nil {
			if !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
				log.Printf("⚠️ 读取麦克风失败: %v", err)
			}
			return
		}
	}
}

// convertChunk 把一段 S32/4ch/48k 转成 16k/mono/S16。
// 末尾不足一个降采样组的字节会被丢弃（<16 字节的零头，无影响）。
func convertChunk(in []byte) []byte {
	frames := len(in) / micInFrame
	groups := frames / micDecimate
	if groups == 0 {
		return nil
	}
	res := make([]byte, 0, groups*2)

	for g := 0; g < groups; g++ {
		var acc int64
		for d := 0; d < micDecimate; d++ {
			base := (g*micDecimate + d) * micInFrame
			// 只取通道 0。实测四通道平均会把信号拉低（通道间有相位差/个别通道是噪音，
			// 通道 0/1/3 单独识别都对，通道 2 是噪音）。
			off := base
			v := int32(uint32(in[off]) | uint32(in[off+1])<<8 |
				uint32(in[off+2])<<16 | uint32(in[off+3])<<24)
			acc += int64(v)
		}
		avg := acc / micDecimate
		// 低 24 位有效 → 右移 8 位得到 16bit 量级
		s := avg >> 8
		if s > math.MaxInt16 {
			s = math.MaxInt16
		} else if s < math.MinInt16 {
			s = math.MinInt16
		}
		res = append(res, byte(uint16(int16(s))), byte(uint16(int16(s))>>8))
	}
	return res
}

// Stop 停止采集并杀掉 arecord。
func (m *MicStreamer) Stop() {
	m.once.Do(func() {
		close(m.stopCh)
		if m.cmd.Process != nil {
			_ = m.cmd.Process.Kill()
		}
	})
	select {
	case <-m.done:
	default:
	}
}
