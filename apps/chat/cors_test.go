package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func newCORSApp(origins []string) *appRuntime {
	cfg := defaultConfig()
	cfg.CORS.AllowedOrigins = origins
	speaker := NewSpeaker()
	return newAppRuntime("config.yaml", cfg, speaker, NewEngine(cfg, speaker))
}

func TestCORSPreflightAllowedByDefault(t *testing.T) {
	app := newCORSApp(nil)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodOptions, "/admin/api/config", nil)
	req.Header.Set("Origin", "http://localhost:5173")

	if applyCORS(app, rec, req) {
		t.Fatal("preflight should be terminated by applyCORS")
	}
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Fatalf("expected wildcard origin, got %q", got)
	}
	if got := rec.Header().Get("Access-Control-Allow-Headers"); got == "" {
		t.Fatal("missing Access-Control-Allow-Headers")
	}
}

func TestCORSAllowListEchoesMatchingOrigin(t *testing.T) {
	app := newCORSApp([]string{"http://localhost:5173"})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/admin/api/config", nil)
	req.Header.Set("Origin", "http://localhost:5173")
	if !applyCORS(app, rec, req) {
		t.Fatal("normal request should not be terminated")
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "http://localhost:5173" {
		t.Fatalf("expected echoed origin, got %q", got)
	}
}

func TestCORSRejectsUnlistedOrigin(t *testing.T) {
	app := newCORSApp([]string{"http://localhost:5173"})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/admin/api/config", nil)
	req.Header.Set("Origin", "http://evil.example")
	applyCORS(app, rec, req)
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("unlisted origin must not be allowed, got %q", got)
	}
}
