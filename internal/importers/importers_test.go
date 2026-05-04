package importers

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRenderStaticImporter(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "render.yaml"), `services:
  - type: web
    name: Docs Site
    runtime: static
    rootDir: web
    buildCommand: npm run build
    staticPublishPath: dist
    envVars:
      - key: API_BASE
        value: https://api.example.com
`)
	res, err := Render(dir)
	if err != nil {
		t.Fatal(err)
	}
	if res.Source != "render" {
		t.Fatalf("source = %q", res.Source)
	}
	m := res.Manifest
	if m.Name != "docs-site" || m.Form != "static" || m.Root != "web/dist" || m.Build != "cd 'web' && npm run build" {
		t.Fatalf("manifest = %+v", m.Component)
	}
	if got := m.Env["API_BASE"]; got != "https://api.example.com" {
		t.Fatalf("API_BASE = %q", got)
	}
}

func TestRenderNodeWebImporterGeneratesDockerfile(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "render.yaml"), `services:
  - type: web
    name: api
    runtime: node
    buildCommand: npm run build
    startCommand: node server.js
    envVars:
      - key: PORT
        value: "3333"
      - key: SESSION_SECRET
        generateValue: true
`)
	res, err := Render(filepath.Join(dir, "render.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Manifest.Form != "web-service" || res.Manifest.Port != 3333 {
		t.Fatalf("manifest = %+v", res.Manifest.Component)
	}
	if _, ok := res.ExtraFiles["Dockerfile"]; !ok {
		t.Fatalf("expected generated Dockerfile")
	}
	if !strings.Contains(string(res.ExtraFiles["Dockerfile"]), `CMD ["sh", "-lc", "node server.js"]`) {
		t.Fatalf("Dockerfile did not preserve start command:\n%s", res.ExtraFiles["Dockerfile"])
	}
	if len(res.Warnings) == 0 || !strings.Contains(strings.Join(res.Warnings, "\n"), "SESSION_SECRET") {
		t.Fatalf("expected secret warning, got %#v", res.Warnings)
	}
}

func TestVercelStaticImporter(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "vercel.json"), `{
  "name": "marketing-site",
  "framework": "vite",
  "buildCommand": "pnpm build",
  "outputDirectory": "dist",
  "env": {"PUBLIC_API": "https://api.example.com"},
  "redirects": [{"source": "/old", "destination": "/new"}]
}`)
	res, err := Vercel(dir)
	if err != nil {
		t.Fatal(err)
	}
	m := res.Manifest
	if m.Name != "marketing-site" || m.Form != "static" || m.Root != "dist" || m.Build != "pnpm build" {
		t.Fatalf("manifest = %+v", m.Component)
	}
	if got := m.Env["PUBLIC_API"]; got != "https://api.example.com" {
		t.Fatalf("PUBLIC_API = %q", got)
	}
	if !strings.Contains(strings.Join(res.Warnings, "\n"), "redirects dropped") {
		t.Fatalf("expected redirect warning, got %#v", res.Warnings)
	}
}

func TestNixFlakeImporterGeneratesDockerfile(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "flake.nix"), `{
  description = "Tiny Nix Web";
  outputs = { self, nixpkgs }: {
    packages.x86_64-linux.default = nixpkgs.legacyPackages.x86_64-linux.writeShellApplication {
      name = "tiny-nix-web";
      text = "echo hello";
    };
  };
}`)
	res, err := NixFlake(dir)
	if err != nil {
		t.Fatal(err)
	}
	if res.Manifest.Name != "tiny-nix-web" || res.Manifest.Form != "web-service" || res.Manifest.Port != 8080 {
		t.Fatalf("manifest = %+v", res.Manifest.Component)
	}
	if !strings.Contains(string(res.ExtraFiles["Dockerfile"]), "nix-command flakes") {
		t.Fatalf("unexpected Dockerfile:\n%s", res.ExtraFiles["Dockerfile"])
	}
}

func writeTestFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
