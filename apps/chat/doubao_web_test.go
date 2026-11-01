package main

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// TestDoubaoWebChatLive 真实调用豆包网页接口。
// 需要先扫码登录（doubao-session.json 存在）并显式开启：
//
//	DOUBAO_LIVE=1 go test -run TestDoubaoWebChatLive -v
func TestDoubaoWebChatLive(t *testing.T) {
	if os.Getenv("DOUBAO_LIVE") == "" {
		t.Skip("设置 DOUBAO_LIVE=1 才会真的请求豆包")
	}
	d := NewDoubaoWeb("doubao-session.json")
	if !d.LoggedIn() {
		t.Skip("未登录：先调 /admin/api/doubao/web/qr 扫码")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	var deltas []string
	ans, err := d.ChatStream(ctx, "用一句话介绍你自己，不超过20个字", func(s string) {
		deltas = append(deltas, s)
	})
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	if strings.TrimSpace(ans) == "" {
		t.Fatalf("回答为空（收到 %d 个增量）", len(deltas))
	}
	t.Logf("增量块数=%d 总字数=%d", len(deltas), len([]rune(ans)))
	t.Logf("回答: %s", ans)
}

// TestDoubaoWebListJobsLive 真实读取豆包定时任务列表。
//	DOUBAO_LIVE=1 go test -run TestDoubaoWebListJobsLive -v
func TestDoubaoWebListJobsLive(t *testing.T) {
	if os.Getenv("DOUBAO_LIVE") == "" {
		t.Skip("设置 DOUBAO_LIVE=1 才会真的请求豆包")
	}
	d := NewDoubaoWeb("doubao-session.json")
	if !d.LoggedIn() {
		t.Skip("未登录")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	jobs, err := d.ListJobs(ctx)
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	t.Logf("共 %d 个任务", len(jobs))
	for _, j := range jobs {
		t.Logf("  id=%s type=%d status=%d last_run=%q conv=%s title=%q",
			j.ID, j.Type, j.Status, j.LastRunTime, j.ConversationID, j.Title)
	}
	if len(jobs) > 0 {
		conv := jobs[0].ConversationID
		if conv != "" {
			msg, err := d.LatestAssistantMessage(ctx, conv)
			if err != nil {
				t.Logf("LatestAssistantMessage(%s): %v", conv, err)
			} else if msg == nil {
				t.Logf("会话 %s 暂无助手消息", conv)
			} else {
				t.Logf("最新消息 id=%s text=%.120s", msg.ID, msg.Text)
			}
		}
	}
}
