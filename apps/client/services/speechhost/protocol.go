// Package speechhost 实现 mips 的语音 socket 协议（speech.usock）。
//
// 参考 stevenjoezhang/xiaoai-agent 的逆向结果：unix datagram + protobuf。
// 上行：REGISTER / STREAM_PREPARE / STREAM_TRANSMITTING / STREAM_END
// 下行：REGISTER_RESPONSE / STREAM_PREPARE_RESPONSE / STOP_CAPTURE / DIALOG_FINISH
package speechhost

import (
	"fmt"
)

// 上下行消息编号（与固件 protobuf 定义一致）
const (
	speechTypeUp   = 0
	speechTypeDown = 1

	upRegister        = 0
	upStreamPrepare   = 1
	upStreamCancel    = 2
	upStreamTransmit  = 3
	upStreamEnd       = 4
	upVoip            = 5

	downRegisterResponse = 0
	downPrepareResponse  = 1
	downStopCapture      = 2
	downDialogFinish     = 5
	downEnableWakeup     = 9

	streamWakeup    = 0
	streamWakeupEnd = 1
	streamASR       = 2
)

// ---- protobuf 编解码（只覆盖本协议用到的 wire type）----

func encVarint(v uint64) []byte {
	var out []byte
	for v >= 0x80 {
		out = append(out, byte(v)|0x80)
		v >>= 7
	}
	return append(out, byte(v))
}

func decVarint(buf []byte) (uint64, int) {
	var v uint64
	for i := 0; i < len(buf) && i < 10; i++ {
		v |= uint64(buf[i]&0x7f) << (7 * i)
		if buf[i]&0x80 == 0 {
			return v, i + 1
		}
	}
	return 0, 0
}

func fieldVarint(num int, v uint64) []byte {
	return append(encVarint(uint64(num)<<3), encVarint(v)...)
}

func fieldBytes(num int, b []byte) []byte {
	out := append(encVarint(uint64(num)<<3|2), encVarint(uint64(len(b)))...)
	return append(out, b...)
}

type field struct {
	num  int
	kind int
	v    uint64
	b    []byte
}

func parseFields(buf []byte) ([]field, error) {
	var out []field
	for len(buf) > 0 {
		key, n := decVarint(buf)
		if n == 0 {
			return out, fmt.Errorf("truncated key")
		}
		buf = buf[n:]
		num, wire := int(key>>3), int(key&7)
		switch wire {
		case 0:
			v, m := decVarint(buf)
			if m == 0 {
				return out, fmt.Errorf("truncated varint")
			}
			buf = buf[m:]
			out = append(out, field{num: num, kind: 0, v: v})
		case 2:
			l, m := decVarint(buf)
			if m == 0 || int(l) > len(buf)-m {
				return out, fmt.Errorf("truncated bytes")
			}
			buf = buf[m:]
			out = append(out, field{num: num, kind: 2, b: buf[:l]})
			buf = buf[l:]
		case 1:
			if len(buf) < 8 {
				return out, fmt.Errorf("truncated fixed64")
			}
			buf = buf[8:]
		case 5:
			if len(buf) < 4 {
				return out, fmt.Errorf("truncated fixed32")
			}
			buf = buf[4:]
		default:
			return out, fmt.Errorf("unsupported wire type %d", wire)
		}
	}
	return out, nil
}

func getVarint(fs []field, num int) (uint64, bool) {
	for _, f := range fs {
		if f.kind == 0 && f.num == num {
			return f.v, true
		}
	}
	return 0, false
}

func getBytes(fs []field, num int) ([]byte, bool) {
	for _, f := range fs {
		if f.kind == 2 && f.num == num {
			return f.b, true
		}
	}
	return nil, false
}

// ---- 帧构造 ----

func downward(downType uint64, dialogID string, payloadField int, payload []byte) []byte {
	body := fieldVarint(1, downType)
	if dialogID != "" {
		body = append(body, fieldBytes(2, []byte(dialogID))...)
	}
	if payload != nil {
		body = append(body, fieldBytes(payloadField, payload)...)
	}
	return append(fieldVarint(1, speechTypeDown), fieldBytes(3, body)...)
}

// ---- 上行帧解析 ----

type upwardMsg struct {
	Type       uint64 // register / prepare / transmit / end ...
	Vendor     string
	Codec      string
	Activate   uint64
	Interact   uint64
	StreamType uint64
	Data       []byte
}

func parseUpward(buf []byte) (*upwardMsg, error) {
	fs, err := parseFields(buf)
	if err != nil {
		return nil, err
	}
	if t, ok := getVarint(fs, 1); !ok || t != speechTypeUp {
		return nil, fmt.Errorf("not an upward message")
	}
	body, ok := getBytes(fs, 2)
	if !ok {
		return nil, fmt.Errorf("missing body")
	}
	bfs, err := parseFields(body)
	if err != nil {
		return nil, err
	}
	upType, ok := getVarint(bfs, 1)
	if !ok {
		return nil, fmt.Errorf("missing upward type")
	}
	msg := &upwardMsg{Type: upType}
	switch upType {
	case upRegister:
		if b, ok := getBytes(bfs, 2); ok {
			rf, _ := parseFields(b)
			if v, ok := getBytes(rf, 1); ok {
				msg.Vendor = string(v)
			}
			if c, ok := getBytes(rf, 2); ok {
				msg.Codec = string(c)
			}
		}
	case upStreamPrepare:
		if b, ok := getBytes(bfs, 3); ok {
			pf, _ := parseFields(b)
			msg.Activate, _ = getVarint(pf, 1)
			msg.Interact, _ = getVarint(pf, 2)
		}
	case upStreamTransmit:
		if b, ok := getBytes(bfs, 4); ok {
			tf, _ := parseFields(b)
			msg.StreamType, _ = getVarint(tf, 1)
			msg.Data, _ = getBytes(tf, 2)
		}
	}
	return msg, nil
}
