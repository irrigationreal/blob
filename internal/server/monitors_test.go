package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/irrigationreal/blob/internal/api"
)

func TestMonitorURLWithPath(t *testing.T) {
	got := monitorURLWithPath("https://app.example.com/base?x=1", "/healthz")
	if got != "https://app.example.com/healthz" {
		t.Fatalf("url = %q", got)
	}
	got = monitorURLWithPath("https://app.example.com", "ready")
	if got != "https://app.example.com/ready" {
		t.Fatalf("url = %q", got)
	}
}

func TestSendMonitorAlertPayload(t *testing.T) {
	var payload map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Fatalf("method = %s", r.Method)
		}
		if got := r.Header.Get("User-Agent"); got != "blob-monitor/1" {
			t.Fatalf("user-agent = %q", got)
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
	}))
	defer srv.Close()

	mon := &api.Monitor{
		Name:         "demo",
		App:          "demo-app",
		URL:          "https://demo.example.com/healthz",
		AlertWebhook: srv.URL,
		LastCheck: api.RouteHealth{
			CheckedAt:  time.Date(2026, 5, 4, 10, 0, 0, 0, time.UTC),
			StatusCode: 503,
			Error:      "HTTP 503, expected 200",
		},
	}
	if err := sendMonitorAlert(context.Background(), mon, "up", "down"); err != nil {
		t.Fatal(err)
	}
	if payload["monitor"] != "demo" || payload["app"] != "demo-app" || payload["previous_status"] != "up" || payload["status"] != "down" {
		t.Fatalf("payload = %#v", payload)
	}
	if payload["error"] != "HTTP 503, expected 200" {
		t.Fatalf("error = %#v", payload["error"])
	}
}
