package server

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/irrigationreal/blob/internal/api"
)

// stubS3 records every PUT/GET/HEAD/DELETE/LIST against a fake bucket. It's
// just enough to exercise the shipper round-trip without pulling in a real
// MinIO. Path-style requests (which we force on for non-AWS endpoints) put
// the bucket in the URL path: PUT /<bucket>/<key>.
type stubS3 struct {
	mu      sync.Mutex
	objects map[string][]byte // key -> body
	bucket  string
	puts    int
	deletes int
}

func newStubS3(bucket string) *stubS3 {
	return &stubS3{bucket: bucket, objects: map[string][]byte{}}
}

func (s *stubS3) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		// Strip leading bucket path component.
		path := strings.TrimPrefix(r.URL.Path, "/"+s.bucket)
		path = strings.TrimPrefix(path, "/")

		// LIST: GET /bucket/?prefix=...
		if r.Method == "GET" && r.URL.Path == "/"+s.bucket && r.URL.Query().Has("list-type") {
			prefix := r.URL.Query().Get("prefix")
			w.Header().Set("Content-Type", "application/xml")
			fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?><ListBucketResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><IsTruncated>false</IsTruncated>`)
			fmt.Fprintf(w, "<Name>%s</Name><Prefix>%s</Prefix><KeyCount>0</KeyCount>", s.bucket, prefix)
			for k, v := range s.objects {
				if !strings.HasPrefix(k, prefix) {
					continue
				}
				fmt.Fprintf(w, "<Contents><Key>%s</Key><Size>%d</Size><LastModified>2026-05-03T00:00:00Z</LastModified></Contents>", k, len(v))
			}
			fmt.Fprint(w, `</ListBucketResult>`)
			return
		}
		switch r.Method {
		case "HEAD":
			// HeadBucket
			if path == "" || path == "/" {
				w.WriteHeader(http.StatusOK)
				return
			}
			if _, ok := s.objects[path]; ok {
				w.WriteHeader(http.StatusOK)
				return
			}
			w.WriteHeader(http.StatusNotFound)
		case "PUT":
			body, _ := io.ReadAll(r.Body)
			s.objects[path] = body
			s.puts++
			w.Header().Set("ETag", `"deadbeef"`)
			w.WriteHeader(http.StatusOK)
		case "GET":
			b, ok := s.objects[path]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Length", fmt.Sprint(len(b)))
			w.WriteHeader(http.StatusOK)
			w.Write(b)
		case "DELETE":
			delete(s.objects, path)
			s.deletes++
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	})
}

// makeStubServer wires a Server pointed at a temp StateDir; no Nomad/Docker
// dependencies are touched (we exercise only the shipper / config code paths).
func makeStubServer(t *testing.T) *Server {
	t.Helper()
	dir := t.TempDir()
	cfg := DefaultConfig()
	cfg.StateDir = dir
	cfg.JobsDir = dir + "/jobs"
	cfg.SourcesDir = dir + "/sources"
	cfg.SecretsDir = dir + "/secrets"
	cfg.SecretKey = dir + "/key"
	srv, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Close)
	return srv
}

func writeBackupFile(t *testing.T, dir, name string, body []byte) string {
	t.Helper()
	full := filepath.Join(dir, name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, body, 0o600); err != nil {
		t.Fatal(err)
	}
	return full
}

func TestShipBackupUploadsFileAndChecksumSidecar(t *testing.T) {
	stub := newStubS3("blob-backups")
	ts := httptest.NewServer(stub.handler())
	defer ts.Close()

	srv := makeStubServer(t)
	cfg := &api.PostgresBackupConfig{
		Instance:          "demo",
		DestinationKind:   "s3",
		S3Endpoint:        ts.URL,
		S3Region:          "us-east-1",
		S3Bucket:          "blob-backups",
		S3Prefix:          "demo/",
		S3AccessKeyID:     "test",
		S3SecretAccessKey: "test",
		S3UsePathStyle:    true,
		Enabled:           true,
	}
	body := []byte("pretend gzipped pg_dump output\n")
	local := writeBackupFile(t, srv.postgresBackupsDir("demo"), "2026-05-03T19-00-00Z.sql.gz", body)

	url, sha, err := srv.shipBackup(t.Context(), cfg, local)
	if err != nil {
		t.Fatalf("shipBackup: %v", err)
	}
	if !strings.HasSuffix(url, "demo/2026-05-03T19-00-00Z.sql.gz") {
		t.Errorf("unexpected url: %q", url)
	}
	want := sha256.Sum256(body)
	if sha != hex.EncodeToString(want[:]) {
		t.Errorf("sha mismatch: got %q want %q", sha, hex.EncodeToString(want[:]))
	}
	// File and sidecar should both be in the stub bucket.
	if _, ok := stub.objects["demo/2026-05-03T19-00-00Z.sql.gz"]; !ok {
		t.Error("backup file not uploaded")
	}
	side, ok := stub.objects["demo/2026-05-03T19-00-00Z.sql.gz.sha256"]
	if !ok {
		t.Fatal("sha256 sidecar not uploaded")
	}
	if !strings.HasPrefix(string(side), hex.EncodeToString(want[:])) {
		t.Errorf("sidecar contents mismatch: %q", string(side))
	}
}

func TestListRemoteBackupsRoundtrips(t *testing.T) {
	stub := newStubS3("b")
	stub.objects["pfx/2026-05-03T19-00-00Z.sql.gz"] = []byte("a")
	stub.objects["pfx/2026-05-03T19-05-00Z.sql.gz"] = []byte("bb")
	stub.objects["pfx/garbage.txt"] = []byte("ignored")
	ts := httptest.NewServer(stub.handler())
	defer ts.Close()
	srv := makeStubServer(t)
	cfg := &api.PostgresBackupConfig{
		Instance: "demo", S3Endpoint: ts.URL, S3Region: "us-east-1",
		S3Bucket: "b", S3Prefix: "pfx/",
		S3AccessKeyID: "k", S3SecretAccessKey: "s",
		S3UsePathStyle: true, Enabled: true,
	}
	got, err := srv.listRemoteBackups(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Errorf("expected 2 .sql.gz entries, got %d (%v)", len(got), got)
	}
	if got["2026-05-03T19-00-00Z.sql.gz"] != 1 || got["2026-05-03T19-05-00Z.sql.gz"] != 2 {
		t.Errorf("sizes off: %v", got)
	}
}

func TestRetentionDecisionDailyOnly(t *testing.T) {
	// 5 backups, one per day across 5 consecutive days. retention_daily=3
	// must keep the 3 newest, drop the 2 oldest. weekly=0/monthly=0.
	names := []string{
		"2026-05-01T03-00-00Z.sql.gz",
		"2026-05-02T03-00-00Z.sql.gz",
		"2026-05-03T03-00-00Z.sql.gz",
		"2026-05-04T03-00-00Z.sql.gz",
		"2026-05-05T03-00-00Z.sql.gz",
	}
	keep := retentionDecision(names, 3, 0, 0)
	want := map[string]bool{
		"2026-05-03T03-00-00Z.sql.gz": true,
		"2026-05-04T03-00-00Z.sql.gz": true,
		"2026-05-05T03-00-00Z.sql.gz": true,
	}
	if len(keep) != len(want) {
		t.Fatalf("kept %d, want %d (%v)", len(keep), len(want), keep)
	}
	for k := range want {
		if _, ok := keep[k]; !ok {
			t.Errorf("expected to keep %q", k)
		}
	}
}

func TestRetentionDecisionMultiplePerDay(t *testing.T) {
	// 6 backups in 2 distinct days; retention_daily=2 keeps the newest in
	// each day (2 total), drops the rest.
	names := []string{
		"2026-05-03T01-00-00Z.sql.gz",
		"2026-05-03T05-00-00Z.sql.gz",
		"2026-05-03T09-00-00Z.sql.gz",
		"2026-05-04T01-00-00Z.sql.gz",
		"2026-05-04T05-00-00Z.sql.gz",
		"2026-05-04T09-00-00Z.sql.gz",
	}
	keep := retentionDecision(names, 2, 0, 0)
	if len(keep) != 2 {
		t.Fatalf("kept %d, want 2 (%v)", len(keep), keep)
	}
	for _, expected := range []string{"2026-05-03T09-00-00Z.sql.gz", "2026-05-04T09-00-00Z.sql.gz"} {
		if _, ok := keep[expected]; !ok {
			t.Errorf("expected to keep %q", expected)
		}
	}
}

func TestRetentionDecisionWeeklyMonthlyUnion(t *testing.T) {
	// One backup per month for 8 months. daily=2, weekly=4, monthly=6.
	// Daily picks the 2 newest dates. Weekly picks 4 distinct weeks.
	// Monthly picks 6 distinct months. We expect the union to be 6 (every
	// month is also a distinct week and the two newest dates fall inside
	// those weeks).
	var names []string
	for m := 1; m <= 8; m++ {
		names = append(names, fmt.Sprintf("2026-%02d-15T03-00-00Z.sql.gz", m))
	}
	keep := retentionDecision(names, 2, 4, 6)
	// Newest 6 months must survive. Months 1 and 2 (oldest) must be pruned.
	for _, n := range names[:2] {
		if _, ok := keep[n]; ok {
			t.Errorf("did not expect to keep oldest %q", n)
		}
	}
	for _, n := range names[2:] {
		if _, ok := keep[n]; !ok {
			t.Errorf("expected to keep %q", n)
		}
	}
}

func TestRetentionDecisionUnparseableNamesAreKept(t *testing.T) {
	names := []string{
		"2026-05-03T03-00-00Z.sql.gz",
		"some-weird-name.sql.gz", // not in our format; defensively keep
	}
	keep := retentionDecision(names, 1, 0, 0)
	if _, ok := keep["some-weird-name.sql.gz"]; !ok {
		t.Error("unparseable filename should be kept (defensive)")
	}
}

func TestParseBackupTime(t *testing.T) {
	cases := map[string]bool{
		"2026-05-03T19-00-00Z.sql.gz":   true,
		"2026-05-03T19-00-00.sql.gz":    false, // missing Z
		"garbage.sql.gz":                false,
	}
	for name, ok := range cases {
		_, err := parseBackupTime(name)
		if (err == nil) != ok {
			t.Errorf("parseBackupTime(%q) = %v; expected ok=%t", name, err, ok)
		}
	}
}

func TestSchedulerReloadIsIdempotent(t *testing.T) {
	srv := makeStubServer(t)
	defer srv.scheduler.Stop()
	// Two reloads on a non-existent config are safe.
	srv.scheduler.Reload("nope")
	srv.scheduler.Reload("nope")
	// Should still be able to add a config and have it picked up without
	// duplicate entries.
	if err := os.MkdirAll(filepath.Join(srv.cfg.StateDir, "postgres", "demo"), 0o700); err != nil {
		t.Fatal(err)
	}
	pm := &postgresMeta{Name: "demo", Version: "16", Database: "demo", User: "blob", Password: "x", Port: 15432, CreatedAt: time.Now()}
	if err := srv.savePostgres(pm); err != nil {
		t.Fatal(err)
	}
	cfg := &api.PostgresBackupConfig{
		Instance: "demo", DestinationKind: "s3", S3Bucket: "b",
		S3AccessKeyID: "k", S3SecretAccessKey: "s",
		Schedule: "0 3 * * *", Enabled: true,
	}
	if err := srv.saveBackupConfig(cfg); err != nil {
		t.Fatal(err)
	}
	srv.scheduler.Reload("demo")
	srv.scheduler.Reload("demo") // idempotent
	srv.scheduler.mu.Lock()
	defer srv.scheduler.mu.Unlock()
	if got := len(srv.scheduler.entries); got != 1 {
		t.Errorf("expected 1 scheduler entry, got %d", got)
	}
}
