package detect

import (
	"os"
	"path/filepath"
	"testing"
)

func mkdir(t *testing.T) string {
	t.Helper()
	return t.TempDir()
}

func write(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDetectDockerfile(t *testing.T) {
	d := mkdir(t)
	write(t, d, "Dockerfile", "FROM alpine\n")
	c, _ := Detect(d)
	if c.Form != "web-service" {
		t.Fatalf("got form %q", c.Form)
	}
}

func TestDetectStaticFromIndex(t *testing.T) {
	d := mkdir(t)
	write(t, d, "index.html", "<h1>hi</h1>")
	c, why := Detect(d)
	if c.Form != "static" || c.Root != "." {
		t.Fatalf("got %+v why=%s", c, why)
	}
}

func TestDetectPackageJSONBuild(t *testing.T) {
	d := mkdir(t)
	write(t, d, "package.json", `{"name":"x","scripts":{"build":"echo build"}}`)
	c, _ := Detect(d)
	if c.Form != "static" || c.Build == "" {
		t.Fatalf("got %+v", c)
	}
}

func TestDetectPnpm(t *testing.T) {
	d := mkdir(t)
	write(t, d, "package.json", `{"scripts":{"build":"vite build"}}`)
	write(t, d, "pnpm-lock.yaml", "lockfileVersion: 6\n")
	c, _ := Detect(d)
	if c.Build == "" || c.Build[:4] != "pnpm" {
		t.Fatalf("expected pnpm build, got %q", c.Build)
	}
}

func TestDefaultName(t *testing.T) {
	cases := map[string]string{
		"hello":          "hello",
		"Hello World":    "hello-world",
		"foo_bar.baz":    "foo-bar-baz",
		"---a---b---":    "a-b",
	}
	for in, want := range cases {
		got := defaultName(in)
		if got != want {
			t.Errorf("defaultName(%q) = %q; want %q", in, got, want)
		}
	}
}
