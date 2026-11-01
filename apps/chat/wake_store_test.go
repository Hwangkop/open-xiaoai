package main

import (
	"path/filepath"
	"testing"
)

func TestMatchWakeReservesXiaoaiTongxueForNative(t *testing.T) {
	store := newWakeStore(filepath.Join(t.TempDir(), "config.yaml"))
	if _, err := store.Add("小爱"); err != nil {
		t.Fatalf("添加唤醒词失败: %v", err)
	}
	e := &Engine{wakeStore: store}
	cfg := &AppConfig{}

	if !e.matchWake("小爱", cfg) {
		t.Fatal("只说「小爱」应唤醒本地链路")
	}
	if !e.matchWake("小爱，今天天气怎么样", cfg) {
		t.Fatal("「小爱+指令」应唤醒本地链路")
	}
	if e.matchWake("小爱同学嗯", cfg) {
		t.Fatal("「小爱同学」是原生唤醒，本地不应抢占")
	}
	if e.matchWake("小爱同学，今天天气怎么样", cfg) {
		t.Fatal("「小爱同学+指令」应留给原生链路")
	}
}
