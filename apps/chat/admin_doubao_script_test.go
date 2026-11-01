package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestDoubaoJSGeneration(t *testing.T) {
	convJS := doubaoConversationsJS(5)
	msgJS := doubaoMessagesJS("123456789", 7)

	for name, js := range map[string]string{"conversations": convJS, "messages": msgJS} {
		if strings.Contains(js, "%!") {
			t.Fatalf("%s JS contains fmt error marker: %s", name, js)
		}
		if !strings.Contains(js, "/im/") {
			t.Fatalf("%s JS missing endpoint", name)
		}
	}
	if !strings.Contains(convJS, "{limit: 5}") {
		t.Fatalf("conversation limit not interpolated: %s", convJS)
	}
	if !strings.Contains(msgJS, "conversation_id: '123456789'") || !strings.Contains(msgJS, "limit: 7") {
		t.Fatalf("message params not interpolated: %s", msgJS)
	}
}

func TestDoubaoMessagesRejectsBadID(t *testing.T) {
	admin := newDoubaoAdmin("http://127.0.0.1:1")
	for _, id := range []string{"", "abc", "1;2", "1 OR 1", "../../x", "１２３"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/admin/api/doubao/messages?id="+url.QueryEscape(id), nil)
		admin.handleDoubaoMessages(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("expected 400 for id %q, got %d body=%s", id, rec.Code, rec.Body.String())
		}
	}
}

func TestClampInt(t *testing.T) {
	cases := []struct {
		raw      string
		fallback int
		want     int
	}{
		{"", 20, 20},
		{"abc", 20, 20},
		{"0", 20, 1},
		{"5", 20, 5},
		{"999", 20, maxMessageLimit},
		{"-3", 20, 1},
	}
	for _, c := range cases {
		if got := clampInt(c.raw, c.fallback, 1, maxMessageLimit); got != c.want {
			t.Fatalf("clampInt(%q)=%d, want %d", c.raw, got, c.want)
		}
	}
}
