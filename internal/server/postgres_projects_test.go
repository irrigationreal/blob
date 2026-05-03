package server

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestPostgresProjectMetaRoundtrip(t *testing.T) {
	original := postgresProjectMeta{
		Instance:           "demo",
		Project:            "app_a",
		Role:               "app_a",
		Database:           "app_a",
		Password:           "abc123",
		StatementTimeoutMS: 30000,
		CreatedAt:          time.Now().UTC().Truncate(time.Second),
	}
	b, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	var loaded postgresProjectMeta
	if err := json.Unmarshal(b, &loaded); err != nil {
		t.Fatal(err)
	}
	if loaded != original {
		t.Errorf("roundtrip mismatch:\n got = %+v\nwant = %+v", loaded, original)
	}
}

func TestParseProjectBinding(t *testing.T) {
	cases := []struct {
		in       string
		instance string
		project  string
	}{
		{"my-pg.payments", "my-pg", "payments"},
		{"demo.app_a", "demo", "app_a"},
		{"demo", "", ""},      // bare instance, no project
		{"", "", ""},
		{".project", "", ""}, // leading dot is invalid (no instance)
	}
	for _, c := range cases {
		i, p := parseProjectBinding(c.in)
		if i != c.instance || p != c.project {
			t.Errorf("parseProjectBinding(%q) = (%q,%q); want (%q,%q)", c.in, i, p, c.instance, c.project)
		}
	}
}

func TestValidProject(t *testing.T) {
	good := []string{"app", "app_a", "app1", "abc_def_123", "ab"}
	for _, g := range good {
		if !validProject(g) {
			t.Errorf("expected %q valid", g)
		}
	}
	bad := []string{"", "a", "1abc", "App", "app-a", "app__", "ABC", "_app", "app$"}
	for _, b := range bad {
		if validProject(b) {
			t.Errorf("expected %q invalid", b)
		}
	}
}

func TestProjectURLMasking(t *testing.T) {
	// Build a Server with a postgres instance meta on disk so projectURL can
	// look up the port.
	dir := t.TempDir()
	cfg := DefaultConfig()
	cfg.StateDir = dir
	cfg.JobsDir = dir + "/jobs"
	cfg.SourcesDir = dir + "/sources"
	cfg.SecretsDir = dir + "/secrets"
	cfg.SecretKey = dir + "/key"
	cfg.PlatformPublicIP = "10.0.0.1"
	srv, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	im := &postgresMeta{Name: "demo", Version: "16", Database: "demo", User: "blob", Password: "super", Port: 15432, CreatedAt: time.Now()}
	if err := srv.savePostgres(im); err != nil {
		t.Fatal(err)
	}
	pm := &postgresProjectMeta{
		Instance: "demo", Project: "app_a", Role: "app_a", Database: "app_a",
		Password: "secret", StatementTimeoutMS: 30000, CreatedAt: time.Now(),
	}
	got := srv.projectURL(pm, false)
	want := "postgres://app_a:secret@10.0.0.1:15432/app_a?sslmode=disable"
	if got != want {
		t.Errorf("project URL = %q; want %q", got, want)
	}
	masked := srv.projectURL(pm, true)
	if !strings.Contains(masked, "***") {
		t.Errorf("masked URL should hide password; got %q", masked)
	}
	if strings.Contains(masked, "secret") {
		t.Errorf("masked URL leaked password: %q", masked)
	}
}

func TestDefaultStatementTimeout(t *testing.T) {
	if defaultStatementTimeoutMS != 30000 {
		t.Fatalf("default statement timeout changed; spec says 30s; got %d", defaultStatementTimeoutMS)
	}
}

func TestSQLLiteralEscaping(t *testing.T) {
	cases := map[string]string{
		"abc":   "'abc'",
		"a'b":   "'a''b'",
		"":      "''",
		"a''b":  "'a''''b'",
	}
	for in, want := range cases {
		got := sqlLiteral(in)
		if got != want {
			t.Errorf("sqlLiteral(%q) = %q; want %q", in, got, want)
		}
	}
}
