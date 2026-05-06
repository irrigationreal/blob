// Package server: ClickHouse managed-service driver (v0.17).
//
// Single-node ClickHouse in OLAP mode. HTTP interface on a static host
// port (15500+ for HTTP, 15600+ for native TCP). Default user `blob`
// with a generated password and a default database matching the
// instance name. Apps bind via `services:` and the resolver injects
// CLICKHOUSE_URL plus the standard CH client env.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/irrigationreal/blob/internal/api"
)

const (
	clickhouseHTTPFloor   = 15500
	clickhouseHTTPCeil    = 15600
	clickhouseNativeFloor = 15600
	clickhouseNativeCeil  = 15700
)

type clickhouseMeta struct {
	Name       string    `json:"name"`
	Version    string    `json:"version"`
	User       string    `json:"user"`
	Password   string    `json:"password"`
	Database   string    `json:"database"`
	HTTPPort   int       `json:"http_port"`
	NativePort int       `json:"native_port"`
	CreatedAt  time.Time `json:"created_at"`
}

func (s *Server) clickhouseMetaDir() string {
	return filepath.Join(s.cfg.StateDir, "clickhouse")
}

func (s *Server) loadClickHouse(name string) (*clickhouseMeta, error) {
	b, err := os.ReadFile(filepath.Join(s.clickhouseMetaDir(), name+".json"))
	if err != nil {
		return nil, err
	}
	m := &clickhouseMeta{}
	if err := json.Unmarshal(b, m); err != nil {
		return nil, err
	}
	return m, nil
}

func (s *Server) saveClickHouse(m *clickhouseMeta) error {
	if err := os.MkdirAll(s.clickhouseMetaDir(), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(s.clickhouseMetaDir(), m.Name+".json"), b, 0o600)
}

func (s *Server) allocateClickHousePorts() (httpPort, nativePort int, err error) {
	usedHTTP := map[int]bool{}
	usedNative := map[int]bool{}
	if entries, e := os.ReadDir(s.clickhouseMetaDir()); e == nil {
		for _, ent := range entries {
			if !strings.HasSuffix(ent.Name(), ".json") {
				continue
			}
			m, e := s.loadClickHouse(strings.TrimSuffix(ent.Name(), ".json"))
			if e == nil {
				usedHTTP[m.HTTPPort] = true
				usedNative[m.NativePort] = true
			}
		}
	}
	httpPort = 0
	for p := clickhouseHTTPFloor; p < clickhouseHTTPCeil; p++ {
		if !usedHTTP[p] {
			httpPort = p
			break
		}
	}
	nativePort = 0
	for p := clickhouseNativeFloor; p < clickhouseNativeCeil; p++ {
		if !usedNative[p] {
			nativePort = p
			break
		}
	}
	if httpPort == 0 || nativePort == 0 {
		return 0, 0, errors.New("clickhouse port pool exhausted")
	}
	return httpPort, nativePort, nil
}

// --- HTTP handlers -----------------------------------------------------------

func (s *Server) handleClickHouse(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "GET":
		out, err := s.listClickHouse(r.Context())
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, out)
	case "POST":
		var req api.CreateClickHouseRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		out, err := s.createClickHouse(r.Context(), &req)
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, out)
	default:
		writeErr(w, 405, "method not allowed")
	}
}

func (s *Server) handleClickHouseItem(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/v1/clickhouse/")
	parts := strings.SplitN(rest, "/", 2)
	name := parts[0]
	if !validName(name) {
		writeErr(w, 400, "invalid name")
		return
	}
	switch {
	case len(parts) == 1 && r.Method == "GET":
		m, err := s.loadClickHouse(name)
		if err != nil {
			writeErr(w, 404, "no such clickhouse")
			return
		}
		writeJSON(w, 200, s.clickhousePublic(r.Context(), m))
	case len(parts) == 1 && r.Method == "DELETE":
		if err := s.destroyClickHouse(r.Context(), name); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, map[string]any{"name": name, "destroyed": true})
	case len(parts) == 2 && parts[1] == "url" && r.Method == "GET":
		m, err := s.loadClickHouse(name)
		if err != nil {
			writeErr(w, 404, "no such clickhouse")
			return
		}
		writeJSON(w, 200, api.ClickHouseURL{URL: s.clickhouseURL(m, false)})
	default:
		writeErr(w, 404, "not found")
	}
}

// --- create / destroy / list --------------------------------------------------

func (s *Server) createClickHouse(ctx context.Context, req *api.CreateClickHouseRequest) (*api.ClickHouse, error) {
	if !validName(req.Name) {
		return nil, errors.New("invalid name")
	}
	if _, err := s.loadClickHouse(req.Name); err == nil {
		return nil, fmt.Errorf("clickhouse %q already exists", req.Name)
	}
	if req.Version == "" {
		req.Version = "24.11"
	}
	if req.CPU <= 0 {
		req.CPU = 500
	}
	if req.Memory <= 0 {
		// ClickHouse memory floor is real — go below 1 GiB and the
		// server will OOM under any non-trivial query.
		req.Memory = 1024
	}
	if req.Database == "" {
		req.Database = req.Name
	}
	httpPort, nativePort, err := s.allocateClickHousePorts()
	if err != nil {
		return nil, err
	}
	m := &clickhouseMeta{
		Name:       req.Name,
		Version:    req.Version,
		User:       "blob",
		Password:   randomPassword(),
		Database:   req.Database,
		HTTPPort:   httpPort,
		NativePort: nativePort,
		CreatedAt:  time.Now(),
	}
	if err := s.saveClickHouse(m); err != nil {
		return nil, err
	}
	id := "clickhouse-" + m.Name
	hcl := renderClickHouseJob(m, s.cfg.Datacenter, id, req.CPU, req.Memory)
	if err := os.MkdirAll(s.cfg.JobsDir, 0o755); err != nil {
		return nil, err
	}
	jobPath := filepath.Join(s.cfg.JobsDir, id+".nomad")
	if err := os.WriteFile(jobPath, []byte(hcl), 0o644); err != nil {
		return nil, err
	}
	if err := s.run(ctx, "nomad", "job", "run", jobPath); err != nil {
		_ = os.Remove(filepath.Join(s.clickhouseMetaDir(), m.Name+".json"))
		return nil, fmt.Errorf("schedule clickhouse: %w", err)
	}
	if err := s.waitJobRunning(ctx, id, 120*time.Second); err != nil {
		return nil, fmt.Errorf("clickhouse %q did not become ready: %w", m.Name, err)
	}
	// The clickhouse-server image's entrypoint reads CLICKHOUSE_DB but
	// hyphenated database names (`demo-ch`) sometimes don't survive the
	// init script's expansion. Create the database explicitly so we
	// don't rely on that path. Idempotent: IF NOT EXISTS.
	if err := s.ensureClickHouseDatabase(ctx, m); err != nil {
		stdLog("clickhouse %s: ensure database %q returned %v (instance is up; create it manually if your client errors with UNKNOWN_DATABASE)", m.Name, m.Database, err)
	}
	return s.clickhousePublic(ctx, m), nil
}

// ensureClickHouseDatabase POSTs `CREATE DATABASE IF NOT EXISTS` over
// the HTTP interface using the bootstrap admin user.
func (s *Server) ensureClickHouseDatabase(ctx context.Context, m *clickhouseMeta) error {
	client := &http.Client{Timeout: 10 * time.Second}
	endpoint := fmt.Sprintf("http://%s:%d/?query=%s",
		s.postgresHost(), m.HTTPPort,
		urlEncodeQuery("CREATE DATABASE IF NOT EXISTS `"+m.Database+"`"),
	)
	req, err := http.NewRequestWithContext(ctx, "POST", endpoint, nil)
	if err != nil {
		return err
	}
	req.SetBasicAuth(m.User, m.Password)
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		body, _ := readAllShort(resp.Body, 512)
		return fmt.Errorf("ch %d: %s", resp.StatusCode, body)
	}
	return nil
}

// urlEncodeQuery is a stripped-down query escaper: spaces → +, otherwise
// pass through. ClickHouse's HTTP query parser tolerates this for the
// CREATE DATABASE shape we use.
func urlEncodeQuery(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == ' ':
			out = append(out, '+')
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9',
			c == '-', c == '_', c == '.', c == '~', c == '`':
			out = append(out, c)
		default:
			out = append(out, '%', "0123456789ABCDEF"[c>>4], "0123456789ABCDEF"[c&15])
		}
	}
	return string(out)
}

func readAllShort(r interface{ Read([]byte) (int, error) }, max int) (string, error) {
	buf := make([]byte, max)
	n, _ := r.Read(buf)
	return string(buf[:n]), nil
}

func (s *Server) destroyClickHouse(ctx context.Context, name string) error {
	m, err := s.loadClickHouse(name)
	if err != nil {
		return errors.New("no such clickhouse")
	}
	id := "clickhouse-" + m.Name
	if err := s.run(ctx, "nomad", "job", "stop", "-purge", id); err != nil {
		stdLog("clickhouse destroy %s: nomad stop returned %v (continuing)", name, err)
	}
	_ = os.Remove(filepath.Join(s.cfg.JobsDir, id+".nomad"))
	_ = os.Remove(filepath.Join(s.clickhouseMetaDir(), m.Name+".json"))
	return nil
}

func (s *Server) listClickHouse(ctx context.Context) (*api.ListClickHouseResponse, error) {
	out := &api.ListClickHouseResponse{}
	entries, err := os.ReadDir(s.clickhouseMetaDir())
	if err != nil {
		if os.IsNotExist(err) {
			return out, nil
		}
		return nil, err
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		m, err := s.loadClickHouse(strings.TrimSuffix(e.Name(), ".json"))
		if err != nil {
			continue
		}
		out.ClickHouse = append(out.ClickHouse, *s.clickhousePublic(ctx, m))
	}
	sort.Slice(out.ClickHouse, func(i, j int) bool { return out.ClickHouse[i].Name < out.ClickHouse[j].Name })
	return out, nil
}

func (s *Server) clickhousePublic(ctx context.Context, m *clickhouseMeta) *api.ClickHouse {
	host := s.postgresHost()
	status := "unknown"
	if body, err := s.nomadGET(ctx, "/v1/job/clickhouse-"+m.Name); err == nil {
		var j struct{ Status string }
		_ = json.Unmarshal(body, &j)
		status = j.Status
	}
	return &api.ClickHouse{
		Name:       m.Name,
		Version:    m.Version,
		Host:       host,
		HTTPPort:   m.HTTPPort,
		NativePort: m.NativePort,
		Database:   m.Database,
		User:       m.User,
		JobID:      "clickhouse-" + m.Name,
		URLMasked:  s.clickhouseURL(m, true),
		Status:     status,
		CreatedAt:  m.CreatedAt,
	}
}

func (s *Server) clickhouseURL(m *clickhouseMeta, mask bool) string {
	pw := m.Password
	if mask {
		pw = "***"
	}
	// HTTP DSN — clients can use either http://host:httpport for the
	// REST interface or clickhouse://host:nativeport for the native
	// protocol. Standard ClickHouse driver libs accept both shapes.
	return fmt.Sprintf("clickhouse://%s:%s@%s:%d/%s", m.User, pw, s.postgresHost(), m.NativePort, m.Database)
}

func (s *Server) lookupClickHouseForBinding(name string, env map[string]string, primary *bool) bool {
	m, err := s.loadClickHouse(name)
	if err != nil {
		return false
	}
	host := s.postgresHost()
	httpURL := fmt.Sprintf("http://%s:%s@%s:%d/", m.User, m.Password, host, m.HTTPPort)
	nativeURL := fmt.Sprintf("clickhouse://%s:%s@%s:%d/%s", m.User, m.Password, host, m.NativePort, m.Database)
	if *primary {
		env["CLICKHOUSE_URL"] = nativeURL
		env["CLICKHOUSE_HTTP_URL"] = httpURL
		env["CLICKHOUSE_HOST"] = host
		env["CLICKHOUSE_PORT"] = fmt.Sprintf("%d", m.NativePort)
		env["CLICKHOUSE_HTTP_PORT"] = fmt.Sprintf("%d", m.HTTPPort)
		env["CLICKHOUSE_USER"] = m.User
		env["CLICKHOUSE_PASSWORD"] = m.Password
		env["CLICKHOUSE_DATABASE"] = m.Database
		*primary = false
	}
	prefix := strings.ToUpper(strings.ReplaceAll(name, "-", "_"))
	env[prefix+"_URL"] = nativeURL
	env[prefix+"_HTTP_URL"] = httpURL
	env[prefix+"_HOST"] = host
	env[prefix+"_PORT"] = fmt.Sprintf("%d", m.NativePort)
	env[prefix+"_USER"] = m.User
	env[prefix+"_PASSWORD"] = m.Password
	env[prefix+"_DATABASE"] = m.Database
	return true
}
