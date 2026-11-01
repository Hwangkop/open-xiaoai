package main

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// newTakeoverTestSpeaker 造一个只测接管状态机的 Speaker：RPC 全部换成内存实现。
func newTakeoverTestSpeaker() (*Speaker, *int32, *int32, *bool) {
	s := NewSpeaker()
	var freezes, unfreezes int32
	stopped := true
	s.nativeReset = func(bool) { atomic.AddInt32(&unfreezes, 1) }
	s.nativeFreeze = func() (int, error) {
		atomic.AddInt32(&freezes, 1)
		stopped = true
		return 4242, nil
	}
	s.nativeUnfreeze = func(pid int) error {
		atomic.AddInt32(&unfreezes, 1)
		stopped = false
		return nil
	}
	s.nativeStateFn = func() (int, bool, error) {
		return 4242, stopped, nil
	}
	return s, &freezes, &unfreezes, &stopped
}

func TestTakeoverBeginAndEnd(t *testing.T) {
	s, freezes, unfreezes, _ := newTakeoverTestSpeaker()

	if s.TakeoverActive() {
		t.Fatal("初始不应在接管中")
	}
	if err := s.BeginNativeTakeover(time.Second); err != nil {
		t.Fatalf("进入接管失败: %v", err)
	}
	if !s.TakeoverActive() {
		t.Fatal("应该进入接管")
	}
	if got := atomic.LoadInt32(freezes); got != 1 {
		t.Fatalf("应冻结 1 次，实际 %d", got)
	}

	// 重复 Begin 幂等
	if err := s.BeginNativeTakeover(time.Second); err != nil {
		t.Fatalf("重复进入接管应成功: %v", err)
	}
	if got := atomic.LoadInt32(freezes); got != 1 {
		t.Fatalf("重复 Begin 不应重复冻结，实际 %d", got)
	}

	s.EndNativeTakeover("测试")
	if s.TakeoverActive() {
		t.Fatal("End 之后不应还在接管")
	}
	// 清理是异步的
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && atomic.LoadInt32(unfreezes) == 0 {
		time.Sleep(10 * time.Millisecond)
	}
	if got := atomic.LoadInt32(unfreezes); got != 1 {
		t.Fatalf("结束接管应触发一次播放器清理，实际 %d", got)
	}

	// 重复 End 幂等
	s.EndNativeTakeover("测试")
	time.Sleep(50 * time.Millisecond)
	if got := atomic.LoadInt32(unfreezes); got != 1 {
		t.Fatalf("重复 End 不应重复清理，实际 %d", got)
	}
}

func TestTakeoverTimeoutAutoUnfreezes(t *testing.T) {
	s, _, unfreezes, _ := newTakeoverTestSpeaker()

	if err := s.BeginNativeTakeover(50 * time.Millisecond); err != nil {
		t.Fatalf("进入接管失败: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if !s.TakeoverActive() {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if s.TakeoverActive() {
		t.Fatal("超时后应自动退出接管")
	}
	time.Sleep(50 * time.Millisecond)
	if got := atomic.LoadInt32(unfreezes); got == 0 {
		t.Fatal("超时后应清理播放器")
	}
}

func TestTakeoverRefreezesWhenConted(t *testing.T) {
	s, freezes, _, _ := newTakeoverTestSpeaker()
	var mu sync.Mutex
	stopped := true
	s.nativeStateFn = func() (int, bool, error) {
		mu.Lock()
		defer mu.Unlock()
		return 4242, stopped, nil
	}

	if err := s.BeginNativeTakeover(time.Second); err != nil {
		t.Fatalf("进入接管失败: %v", err)
	}
	// 模拟被外部 SIGCONT
	mu.Lock()
	stopped = false
	mu.Unlock()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if atomic.LoadInt32(freezes) >= 2 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got := atomic.LoadInt32(freezes); got < 2 {
		t.Fatalf("被外部解冻后应补冻，实际冻结次数 %d", got)
	}
	s.EndNativeTakeover("测试")
}

func TestTakeoverEndsWhenPlayerGone(t *testing.T) {
	s := NewSpeaker()
	s.nativeFreeze = func() (int, error) { return 4242, nil }
	s.nativeUnfreeze = func(int) error { return nil }
	s.nativeStateFn = func() (int, bool, error) { return 0, false, nil }

	if err := s.BeginNativeTakeover(time.Second); err != nil {
		t.Fatalf("进入接管失败: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if !s.TakeoverActive() {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if s.TakeoverActive() {
		t.Fatal("播放器不存在时应结束接管")
	}
}

func TestTakeoverFreezeFailureKeepsIdle(t *testing.T) {
	s := NewSpeaker()
	s.nativeFreeze = func() (int, error) { return 0, errTestFreeze{} }

	if err := s.BeginNativeTakeover(time.Second); err == nil {
		t.Fatal("冻结失败时 Begin 应返回错误")
	}
	if s.TakeoverActive() {
		t.Fatal("冻结失败时不应进入接管")
	}
}

type errTestFreeze struct{}

func (errTestFreeze) Error() string { return "freeze failed" }
