package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newDoubaoAdmin(baseURL string) *adminServer {
	return &adminServer{
		getDoubaoConfig: func() Doubao2APIConfig {
			return Doubao2APIConfig{BaseURL: baseURL}
		},
	}
}

func TestDoubaoHealthDisabledWhenBaseURLEmpty(t *testing.T) {
	admin := newDoubaoAdmin("")
	rec := httptest.NewRecorder()
	admin.handleDoubaoHealth(rec, httptest.NewRequest(http.MethodGet, "/admin/api/doubao/health", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got["enabled"] != false {
		t.Fatalf("expected enabled=false, got %v", got["enabled"])
	}
}

func TestDoubaoHealthMergesUpstream(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"status":"ok","logged_in":true}`))
	}))
	defer upstream.Close()

	admin := newDoubaoAdmin(upstream.URL)
	rec := httptest.NewRecorder()
	admin.handleDoubaoHealth(rec, httptest.NewRequest(http.MethodGet, "/admin/api/doubao/health", nil))

	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got["enabled"] != true || got["reachable"] != true || got["logged_in"] != true {
		t.Fatalf("unexpected merged result: %v", got)
	}
	if got["base_url"] != upstream.URL {
		t.Fatalf("expected base_url echoed, got %v", got["base_url"])
	}
}

func TestDoubaoHealthReportsUnreachable(t *testing.T) {
	admin := newDoubaoAdmin("http://127.0.0.1:1")
	rec := httptest.NewRecorder()
	admin.handleDoubaoHealth(rec, httptest.NewRequest(http.MethodGet, "/admin/api/doubao/health", nil))

	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got["reachable"] != false {
		t.Fatalf("expected reachable=false, got %v", got)
	}
	if msg, _ := got["error"].(string); !strings.Contains(msg, "doubao2api") {
		t.Fatalf("expected friendly error, got %v", got["error"])
	}
}

func TestDoubaoLoginProxiesQRLogin(t *testing.T) {
	var sawAuth string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/session/qr-login" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		sawAuth = r.Header.Get("Authorization")
		switch r.Method {
		case http.MethodPost:
			_, _ = w.Write([]byte(`{"status":"qr_ready","qr_image_base64":"QUJD"}`))
		case http.MethodGet:
			_, _ = w.Write([]byte(`{"status":"success","cookies_count":5}`))
		}
	}))
	defer upstream.Close()

	admin := &adminServer{
		getDoubaoConfig: func() Doubao2APIConfig {
			return Doubao2APIConfig{BaseURL: upstream.URL, APIKey: "sk-test"}
		},
	}

	rec := httptest.NewRecorder()
	admin.handleDoubaoLogin(rec, httptest.NewRequest(http.MethodPost, "/admin/api/doubao/login", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "qr_image_base64") {
		t.Fatalf("unexpected POST result: %d %s", rec.Code, rec.Body.String())
	}
	if sawAuth != "Bearer sk-test" {
		t.Fatalf("expected bearer token forwarded, got %q", sawAuth)
	}

	rec = httptest.NewRecorder()
	admin.handleDoubaoLogin(rec, httptest.NewRequest(http.MethodGet, "/admin/api/doubao/login", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"success"`) {
		t.Fatalf("unexpected GET result: %d %s", rec.Code, rec.Body.String())
	}
}

func TestDoubaoLoginRejectsUnsupportedMethod(t *testing.T) {
	admin := newDoubaoAdmin("http://127.0.0.1:1")
	rec := httptest.NewRecorder()
	admin.handleDoubaoLogin(rec, httptest.NewRequest(http.MethodDelete, "/admin/api/doubao/login", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", rec.Code)
	}
}
