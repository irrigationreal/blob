package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/darvell/blob/internal/api"
)

func testAuditServer(t *testing.T) *Server {
	t.Helper()
	dir := t.TempDir()
	cfg := DefaultConfig()
	cfg.StateDir = dir
	cfg.JobsDir = dir + "/jobs"
	cfg.SourcesDir = dir + "/sources"
	cfg.SecretsDir = dir + "/secrets"
	cfg.SecretKey = dir + "/key"
	cfg.Token = "top-secret-token"
	srv, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Close)
	return srv
}

func TestAuditEventHashChain(t *testing.T) {
	srv := testAuditServer(t)
	first := &api.AuditEvent{ID: "one", CreatedAt: time.Unix(1, 0).UTC(), Actor: "bearer:test", Method: "POST", Path: "/v1/secrets", Action: "create secrets", StatusCode: 200}
	second := &api.AuditEvent{ID: "two", CreatedAt: time.Unix(2, 0).UTC(), Actor: "bearer:test", Method: "DELETE", Path: "/v1/secrets/foo", Action: "delete secrets/foo", StatusCode: 200}
	if err := srv.appendAuditEvent(first); err != nil {
		t.Fatal(err)
	}
	if err := srv.appendAuditEvent(second); err != nil {
		t.Fatal(err)
	}
	events, err := srv.readAuditEvents()
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("events len = %d; want 2", len(events))
	}
	if events[0].PreviousHash != "" {
		t.Fatalf("first previous hash = %q; want empty", events[0].PreviousHash)
	}
	if events[1].PreviousHash != events[0].Hash {
		t.Fatalf("second previous hash = %q; want %q", events[1].PreviousHash, events[0].Hash)
	}
	if events[0].Hash == "" || events[1].Hash == "" || events[0].Hash == events[1].Hash {
		t.Fatalf("bad hashes: %#v", events)
	}
}

func TestAuditRedaction(t *testing.T) {
	allocID := "12345678-1234-1234-1234-123456789abc"
	in := "postgres://user:secret@db:5432/app?sslmode=disable password=hunter2 token=abc " + allocID
	out := redactAuditText(in)
	for _, leaked := range []string{"secret", "hunter2", "abc", allocID, "postgres://user"} {
		if strings.Contains(out, leaked) {
			t.Fatalf("redacted text leaked %q: %q", leaked, out)
		}
	}
}

func TestAuthenticatedMutationIsAuditedWithoutSecretBody(t *testing.T) {
	srv := testAuditServer(t)
	h := srv.Routes()
	body := []byte(`{"name":"demo-secret","environment":"prod","value":"super-secret-value"}`)
	req := httptest.NewRequest("POST", "/v1/secrets", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer top-secret-token")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "demo postgres://user:secret@host/db 12345678-1234-1234-1234-123456789abc")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("secret set status = %d body=%s", w.Code, w.Body.String())
	}

	req = httptest.NewRequest("GET", "/v1/audit", nil)
	req.Header.Set("Authorization", "Bearer top-secret-token")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("audit list status = %d body=%s", w.Code, w.Body.String())
	}
	var out api.ListAuditResponse
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Events) != 1 {
		t.Fatalf("events len = %d; want 1", len(out.Events))
	}
	raw := w.Body.String()
	for _, leaked := range []string{"super-secret-value", "top-secret-token", "postgres://user", "12345678-1234-1234-1234-123456789abc"} {
		if strings.Contains(raw, leaked) {
			t.Fatalf("audit output leaked %q: %s", leaked, raw)
		}
	}
	if out.Events[0].Method != "POST" || out.Events[0].Path != "/v1/secrets" || out.Events[0].StatusCode != http.StatusOK {
		t.Fatalf("bad event: %+v", out.Events[0])
	}
}
