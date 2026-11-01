package main

import "testing"

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
		if got := parseRouterAction(c.reply); got != c.want {
			t.Errorf("parseRouterAction(%q) = %q, want %q", c.reply, got, c.want)
		}
	}
}
