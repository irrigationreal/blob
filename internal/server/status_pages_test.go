package server

import (
	"strings"
	"testing"
	"time"

	"github.com/darvell/blob/internal/api"
)

func TestPublicStatusPagePath(t *testing.T) {
	app, asJSON := publicStatusPagePath("/status/blob-mongo-demo.json")
	if app != "blob-mongo-demo" || !asJSON {
		t.Fatalf("json path = (%q,%t); want blob-mongo-demo,true", app, asJSON)
	}
	app, asJSON = publicStatusPagePath("/status/blob-mongo-demo")
	if app != "blob-mongo-demo" || asJSON {
		t.Fatalf("html path = (%q,%t); want blob-mongo-demo,false", app, asJSON)
	}
	app, _ = publicStatusPagePath("/status/blob-mongo-demo/allocs")
	if app != "" {
		t.Fatalf("nested path should not resolve an app, got %q", app)
	}
}

func TestStatusPageMetaRoundtrip(t *testing.T) {
	dir := t.TempDir()
	cfg := DefaultConfig()
	cfg.StateDir = dir
	cfg.JobsDir = dir + "/jobs"
	cfg.SourcesDir = dir + "/sources"
	cfg.SecretsDir = dir + "/secrets"
	cfg.SecretKey = dir + "/key"
	cfg.BaseDomain = "example.com"
	srv, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	binding := &api.StatusPageBinding{App: "demo", CreatedAt: time.Now().UTC().Truncate(time.Second)}
	if err := srv.saveStatusPage(binding); err != nil {
		t.Fatal(err)
	}
	loaded, err := srv.loadStatusPage("demo")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.App != "demo" || loaded.URL != "https://blob.example.com/status/demo" {
		t.Fatalf("loaded = %+v", loaded)
	}
}

func TestPublicStatusDoesNotExposeAllocationIDs(t *testing.T) {
	allocID := "12345678-1234-1234-1234-123456789abc"
	got := sanitizePublicText("failed allocation " + allocID)
	if strings.Contains(got, allocID) {
		t.Fatalf("sanitized text leaked allocation id: %q", got)
	}
}

func TestRenderStatusPageHTMLEscapesAppName(t *testing.T) {
	page := &api.PublicStatusPage{
		App:         "demo<script>",
		Overall:     "operational",
		GeneratedAt: time.Unix(0, 0).UTC(),
		AppStatus: api.PublicAppStatus{
			Status: "running",
			URL:    "https://demo.example.com",
		},
		RouteHealth: api.RouteHealth{Status: "reachable", StatusCode: 200},
	}
	html := renderStatusPageHTML(page)
	if strings.Contains(html, "<script>") {
		t.Fatalf("rendered html was not escaped: %s", html)
	}
	if !strings.Contains(html, "Blob status: demo&lt;script&gt;") {
		t.Fatalf("rendered html missing escaped title: %s", html)
	}
}
