package utils

import (
	"runtime"
	"testing"
)

func TestParseStatState(t *testing.T) {
	cases := []struct {
		stat string
		want byte
		ok   bool
	}{
		{"123 (mediaplayer) T 1 2 3", 'T', true},
		{"123 (a b) S 1 2", 'S', true},
		{"123 (x)y) R 4 5", 'R', true}, // comm 里带右括号，要从最后一个 ) 之后取
		{"garbage", 0, false},
		{"1 ()", 0, false},
	}
	for _, c := range cases {
		got, err := parseStatState(c.stat)
		if c.ok {
			if err != nil || got != c.want {
				t.Fatalf("parseStatState(%q) = %q, %v; want %q", c.stat, got, err, c.want)
			}
			continue
		}
		if err == nil {
			t.Fatalf("parseStatState(%q) 应该报错，实际得到 %q", c.stat, got)
		}
	}
}

func TestFindProcessByNameMissing(t *testing.T) {
	if _, err := FindProcessByName("definitely-not-a-real-process-xyz"); err == nil {
		t.Fatal("不存在的进程名应该返回错误")
	}
}

func TestUnfreezeIsIdempotent(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("冻结/解冻仅支持 linux")
	}
	// 不存在的 pid：第一次和第二次都应视为成功
	if err := UnfreezeNativePlayback(999999); err != nil {
		t.Fatalf("解冻不存在的进程应视为成功: %v", err)
	}
	if err := UnfreezeNativePlayback(999999); err != nil {
		t.Fatalf("重复解冻应幂等: %v", err)
	}
}

func TestProcessStateMissing(t *testing.T) {
	if _, err := ProcessState(999999); err == nil {
		t.Fatal("不存在的 pid 应该返回错误")
	}
	if IsProcessStopped(999999) {
		t.Fatal("不存在的进程不应被判为停止态")
	}
}
