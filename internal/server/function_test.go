package server

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/darvell/blob/internal/api"
)

func TestFunctionHandlerDetectsIndexMJS(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.mjs"), []byte("export default () => ({ok:true})"), 0o644); err != nil {
		t.Fatal(err)
	}
	handler, err := functionHandler(dir, ".", "")
	if err != nil {
		t.Fatal(err)
	}
	if handler != "index.mjs" {
		t.Fatalf("handler = %q", handler)
	}
}

func TestFunctionHandlerRejectsTraversal(t *testing.T) {
	if _, err := functionHandler(t.TempDir(), ".", "../index.js"); err == nil {
		t.Fatal("expected traversal rejection")
	}
}

func TestRenderFunctionDockerfile(t *testing.T) {
	dockerfile := renderFunctionDockerfile(".", "index.mjs")
	for _, want := range []string{
		"FROM node:22-alpine",
		`COPY [".","/srv/"]`,
		"BLOB_FUNCTION_HANDLER=\"index.mjs\"",
		"/blob-function/server.mjs",
	} {
		if !strings.Contains(dockerfile, want) {
			t.Fatalf("dockerfile missing %q:\n%s", want, dockerfile)
		}
	}
}

func TestPrepareFunctionBuildWritesGeneratedFiles(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.js"), []byte("module.exports = () => ({ ok: true })"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := &Server{}
	if err := s.prepareFunctionBuild(context.Background(), dir, &api.DeployRequest{Form: "function"}); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"Dockerfile.blob-function", filepath.Join(".blob-function", "server.mjs")} {
		if _, err := os.Stat(filepath.Join(dir, path)); err != nil {
			t.Fatalf("missing %s: %v", path, err)
		}
	}
}
