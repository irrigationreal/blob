package server

import (
	"strings"
	"testing"
)

func TestReplaceAppTaskImageOnlyTouchesPrimaryTask(t *testing.T) {
	hcl := `job "demo" {
  meta {
    blob_projection_hash = "sha256:old"
  }
  group "web" {
    task "app" {
      config {
        image = "registry/demo:bad"
      }
    }
    task "sidecar" {
      config {
        image = "registry/sidecar:keep"
      }
    }
  }
}`
	got, ok := replaceAppTaskImage(hcl, "registry/demo:good")
	if !ok {
		t.Fatal("expected image replacement")
	}
	if !strings.Contains(got, `image = "registry/demo:good"`) {
		t.Fatalf("primary image was not replaced: %s", got)
	}
	if !strings.Contains(got, `image = "registry/sidecar:keep"`) {
		t.Fatalf("sidecar image was changed: %s", got)
	}
}

func TestReplaceProjectionHashInJobFile(t *testing.T) {
	hcl := `meta {
  blob_projection_hash = "sha256:old"
}`
	got, err := replaceProjectionHashInJobFile(hcl, "sha256:new")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, `blob_projection_hash = "sha256:new"`) {
		t.Fatalf("hash not replaced: %s", got)
	}
}
