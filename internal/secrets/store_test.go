package secrets

import (
	"path/filepath"
	"testing"
)

func TestRoundtrip(t *testing.T) {
	dir := t.TempDir()
	s, err := New(dir, filepath.Join(dir, ".key"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Set("prod", "db-url", "postgres://user:pw@host/db"); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get("prod", "db-url")
	if err != nil {
		t.Fatal(err)
	}
	if got != "postgres://user:pw@host/db" {
		t.Fatalf("got %q", got)
	}
	metas, err := s.List("prod")
	if err != nil {
		t.Fatal(err)
	}
	if len(metas) != 1 || metas[0].Name != "db-url" {
		t.Fatalf("metas: %+v", metas)
	}
	if metas[0].Length != len("postgres://user:pw@host/db") {
		t.Fatalf("length wrong: %d", metas[0].Length)
	}
}

func TestEnvironmentsIsolated(t *testing.T) {
	dir := t.TempDir()
	s, _ := New(dir, filepath.Join(dir, ".key"))
	_ = s.Set("prod", "k", "p")
	_ = s.Set("staging", "k", "s")
	if v, _ := s.Get("prod", "k"); v != "p" {
		t.Fatal("prod wrong")
	}
	if v, _ := s.Get("staging", "k"); v != "s" {
		t.Fatal("staging wrong")
	}
}

func TestKeyPersistsAcrossOpens(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, ".key")
	s1, _ := New(dir, keyPath)
	_ = s1.Set("prod", "k", "hello")
	s2, err := New(dir, keyPath)
	if err != nil {
		t.Fatal(err)
	}
	v, err := s2.Get("prod", "k")
	if err != nil || v != "hello" {
		t.Fatalf("got %q err=%v", v, err)
	}
}
