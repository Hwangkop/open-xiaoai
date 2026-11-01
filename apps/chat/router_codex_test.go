package main

import "testing"

func TestParseRouterActionSay(t *testing.T) {
	action, say := parseRouterAction(`好的{"action":"done","say":"好，放《如愿》"}`)
	if action != "done" || say != "好，放《如愿》" {
		t.Fatalf("action=%q say=%q", action, say)
	}
	action, say = parseRouterAction(`{"action":"ignore"}`)
	if action != "ignore" || say != "" {
		t.Fatalf("action=%q say=%q", action, say)
	}
}

func TestParseRouterAction(t *testing.T) {
	cases := []struct {
		reply string
		want  string
	}{
		{`{"action":"done"}`, "done"},
		{"好的\n{\"action\":\"dialog\"}", "dialog"},
		{`{"action":"ignore"}`, "ignore"},
		{"我调了工具\n```json\n{\"action\":\"done\"}\n```", "done"},
		{"", "ignore"},
		{"随便说点什么", "ignore"},
		{`{"action":"啥"}`, "ignore"},
	}
	for _, c := range cases {
		if got, _ := parseRouterAction(c.reply); got != c.want {
			t.Errorf("parseRouterAction(%q) = %q, want %q", c.reply, got, c.want)
		}
	}
}
