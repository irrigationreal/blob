package server

import (
	"strings"
	"testing"
	"time"
)

func TestRenderValkeyJob(t *testing.T) {
	m := &valkeyMeta{
		Name:      "demo-cache",
		Version:   "8",
		Password:  "topsecret",
		Port:      16379,
		CreatedAt: time.Now(),
	}
	hcl := renderValkeyJob(m, "dc1", "valkey-demo-cache", 200, 256)
	for _, want := range []string{
		`job "valkey-demo-cache"`,
		`datacenters = ["dc1"]`,
		`type = "service"`,
		`static = 16379`,
		`to     = 6379`,
		`image = "valkey/valkey:8-alpine"`,
		`source   = "blob-valkey-demo-cache"`,
		`"--requirepass", "topsecret"`,
		`"--appendonly", "yes"`,
		`type     = "tcp"`,
		`cpu    = 200`,
		`memory = 256`,
	} {
		if !strings.Contains(hcl, want) {
			t.Errorf("missing %q\n--- HCL ---\n%s", want, hcl)
		}
	}
}
