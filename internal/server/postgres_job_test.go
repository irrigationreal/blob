package server

import (
	"strings"
	"testing"
	"time"
)

func TestRenderPostgresJob(t *testing.T) {
	m := &postgresMeta{
		Name:      "demo",
		Version:   "16",
		Database:  "demo",
		User:      "blob",
		Password:  "abc123",
		Port:      15432,
		CreatedAt: time.Now(),
	}
	hcl := renderPostgresJob(m, "dc1", "pg-demo")
	for _, want := range []string{
		`job "pg-demo"`,
		`datacenters = ["dc1"]`,
		`type = "service"`,
		`static = 15432`,
		`to     = 5432`,
		`image = "postgres:16-alpine"`,
		`source = "blob-pg-demo"`,
		`POSTGRES_USER     = "blob"`,
		`POSTGRES_PASSWORD = "abc123"`,
		`POSTGRES_DB       = "demo"`,
		`PGDATA            = "/var/lib/postgresql/data/pgdata"`,
		`type     = "tcp"`,
	} {
		if !strings.Contains(hcl, want) {
			t.Errorf("missing %q\n--- HCL ---\n%s", want, hcl)
		}
	}
}
