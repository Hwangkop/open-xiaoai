package main

import (
	"testing"
	"time"
)

func mustLoc(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatalf("时区加载失败: %v", err)
	}
	return loc
}

func TestNextRunDaily(t *testing.T) {
	loc := mustLoc(t)
	from := time.Date(2026, 10, 31, 7, 0, 0, 0, loc)
	next, repeating, err := nextRun("daily 08:00", from, loc)
	if err != nil || !repeating {
		t.Fatalf("err=%v repeating=%v", err, repeating)
	}
	if got := next.Format("2006-01-02 15:04"); got != "2026-10-31 08:00" {
		t.Fatalf("当天应执行，got %s", got)
	}

	from = time.Date(2026, 10, 31, 9, 0, 0, 0, loc)
	next, _, _ = nextRun("daily 08:00", from, loc)
	if got := next.Format("2006-01-02 15:04"); got != "2026-11-01 08:00" {
		t.Fatalf("已过点应顺延到明天，got %s", got)
	}
}

func TestNextRunWeekly(t *testing.T) {
	loc := mustLoc(t)
	// 2026-10-31 是周六
	from := time.Date(2026, 10, 31, 9, 0, 0, 0, loc)
	next, repeating, err := nextRun("weekly mon 08:00", from, loc)
	if err != nil || !repeating {
		t.Fatalf("err=%v repeating=%v", err, repeating)
	}
	if got := next.Format("2006-01-02 15:04 Mon"); got != "2026-11-02 08:00 Mon" {
		t.Fatalf("got %s", got)
	}
}

func TestNextRunEvery(t *testing.T) {
	loc := mustLoc(t)
	from := time.Date(2026, 10, 31, 9, 0, 0, 0, loc)
	next, repeating, err := nextRun("every 30m", from, loc)
	if err != nil || !repeating {
		t.Fatalf("err=%v repeating=%v", err, repeating)
	}
	if d := next.Sub(from); d != 30*time.Minute {
		t.Fatalf("间隔应为 30m，got %v", d)
	}
	if _, _, err := nextRun("every 10s", from, loc); err == nil {
		t.Fatalf("小于 1 分钟应报错或至少夹到 1 分钟")
	}
}

func TestNextRunOnce(t *testing.T) {
	loc := mustLoc(t)
	from := time.Date(2026, 10, 31, 9, 0, 0, 0, loc)
	next, repeating, err := nextRun("once 2026-11-01 08:00", from, loc)
	if err != nil || repeating {
		t.Fatalf("err=%v repeating=%v", err, repeating)
	}
	if got := next.Format("2006-01-02 15:04"); got != "2026-11-01 08:00" {
		t.Fatalf("got %s", got)
	}
	if _, _, err := nextRun("once 2026-10-30 08:00", from, loc); err == nil {
		t.Fatalf("过去的时间应报错")
	}
}

func TestNextRunClockOnly(t *testing.T) {
	loc := mustLoc(t)
	from := time.Date(2026, 10, 31, 9, 30, 0, 0, loc)
	next, _, err := nextRun("08:00", from, loc)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if got := next.Format("01-02 15:04"); got != "11-01 08:00" {
		t.Fatalf("只给时间且已过点应顺延到明天，got %s", got)
	}
}

func TestTaskStoreCRUD(t *testing.T) {
	dir := t.TempDir()
	loc := mustLoc(t)
	store := newTaskStore(dir+"/config.yaml", loc)

	task, err := store.Add("提醒我喝水", "every 10m", false)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if len(store.List()) != 1 {
		t.Fatalf("列表应有 1 条")
	}
	if _, ok := store.Get(task.ID); !ok {
		t.Fatalf("应能按 id 找到")
	}

	// 到点触发一次
	due := store.Due(time.Now().Add(11 * time.Minute))
	if len(due) != 1 {
		t.Fatalf("应有 1 个任务到点，got %d", len(due))
	}
	// 同一时刻再取不应重复
	if again := store.Due(time.Now().Add(11 * time.Minute)); len(again) != 0 {
		t.Fatalf("不应重复触发，got %d", len(again))
	}

	if _, ok := store.SetEnabled(task.ID, false); !ok {
		t.Fatalf("停用失败")
	}
	if !store.Delete(task.ID) {
		t.Fatalf("删除失败")
	}
	if len(store.List()) != 0 {
		t.Fatalf("删除后应为空")
	}
}

func TestTaskStorePersists(t *testing.T) {
	dir := t.TempDir()
	loc := mustLoc(t)
	path := dir + "/config.yaml"

	store := newTaskStore(path, loc)
	if _, err := store.Add("一次性提醒", "once 2030-01-01 08:00", false); err != nil {
		t.Fatalf("Add: %v", err)
	}
	reloaded := newTaskStore(path, loc)
	if len(reloaded.List()) != 1 {
		t.Fatalf("重新加载后应仍有 1 条")
	}
}
