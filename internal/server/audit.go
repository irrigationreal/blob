package server

import (
	"bufio"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/irrigationreal/blob/internal/api"
)

var (
	auditAllocIDRE = regexp.MustCompile(`\b[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}\b`)
	auditHexIDRE   = regexp.MustCompile(`\b[0-9a-fA-F]{32}\b`)
	auditDSNRE     = regexp.MustCompile(`(?i)\b[a-z][a-z0-9+.-]*://[^\s"'<>]+`)
	auditSecretRE  = regexp.MustCompile(`(?i)(password|passwd|pwd|token|secret|key|dsn|url)=([^&\s]+)`)
)

func (s *Server) auditDir() string {
	return filepath.Join(s.cfg.StateDir, "audit")
}

func (s *Server) auditPath() string {
	return filepath.Join(s.auditDir(), "events.jsonl")
}

func (s *Server) handleAudit(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		writeErr(w, 405, "method not allowed")
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	events, err := s.listAuditEvents(limit)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, &api.ListAuditResponse{Events: events})
}

func (s *Server) handleAuditItem(w http.ResponseWriter, r *http.Request) {
	id := strings.Trim(strings.TrimPrefix(r.URL.Path, "/v1/audit/"), "/")
	if id == "" || strings.Contains(id, "/") {
		writeErr(w, 400, "invalid audit event id")
		return
	}
	if r.Method != "GET" {
		writeErr(w, 405, "method not allowed")
		return
	}
	event, err := s.getAuditEvent(id)
	if err != nil {
		writeErr(w, 404, err.Error())
		return
	}
	writeJSON(w, 200, event)
}

func (s *Server) auditMutatingRequest(r *http.Request, statusCode int, actor string) {
	if !isAuditMutation(r) {
		return
	}
	event := api.AuditEvent{
		ID:         newAuditID(),
		CreatedAt:  time.Now().UTC(),
		Actor:      redactAuditText(actor),
		Method:     r.Method,
		Path:       redactAuditText(r.URL.EscapedPath()),
		Action:     auditAction(r.Method, r.URL.EscapedPath()),
		StatusCode: statusCode,
		RemoteAddr: redactAuditText(remoteHost(r.RemoteAddr)),
		UserAgent:  redactAuditText(r.UserAgent()),
	}
	if err := s.appendAuditEvent(&event); err != nil {
		stdLog("audit append failed for %s %s: %v", r.Method, r.URL.EscapedPath(), err)
	}
}

func isAuditMutation(r *http.Request) bool {
	if !strings.HasPrefix(r.URL.Path, "/v1/") {
		return false
	}
	switch r.Method {
	case "POST", "PUT", "PATCH", "DELETE":
		return true
	default:
		return false
	}
}

func auditAction(method, path string) string {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) < 2 || parts[0] != "v1" {
		return strings.ToLower(method)
	}
	resource := parts[1]
	verb := map[string]string{"POST": "create", "PUT": "update", "PATCH": "update", "DELETE": "delete"}[method]
	if verb == "" {
		verb = strings.ToLower(method)
	}
	if len(parts) >= 3 {
		return verb + " " + resource + "/" + redactAuditText(parts[2])
	}
	return verb + " " + resource
}

func remoteHost(addr string) string {
	host, _, err := net.SplitHostPort(addr)
	if err == nil {
		return host
	}
	return addr
}

func newAuditID() string {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("aud-%d", time.Now().UnixNano())
	}
	return fmt.Sprintf("aud-%d-%s", time.Now().UnixNano(), hex.EncodeToString(b[:]))
}

func auditActor(token string) string {
	sum := sha256.Sum256([]byte(token))
	return "bearer:" + hex.EncodeToString(sum[:])[:12]
}

func (s *Server) appendAuditEvent(event *api.AuditEvent) error {
	s.auditMu.Lock()
	defer s.auditMu.Unlock()

	prev, err := s.lastAuditHashLocked()
	if err != nil {
		return err
	}
	event.PreviousHash = prev
	event.Hash = hashAuditEvent(event)
	if err := os.MkdirAll(s.auditDir(), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(s.auditPath(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	b, err := json.Marshal(event)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(b, '\n')); err != nil {
		return err
	}
	return f.Sync()
}

func (s *Server) lastAuditHashLocked() (string, error) {
	f, err := os.Open(s.auditPath())
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1024*1024)
	last := ""
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var event api.AuditEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			return "", fmt.Errorf("audit chain contains malformed event: %w", err)
		}
		if event.Hash != hashAuditEvent(&event) {
			return "", fmt.Errorf("audit chain hash mismatch at %s", event.ID)
		}
		if event.PreviousHash != last {
			return "", fmt.Errorf("audit chain previous hash mismatch at %s", event.ID)
		}
		last = event.Hash
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}
	return last, nil
}

func hashAuditEvent(event *api.AuditEvent) string {
	copy := *event
	copy.Hash = ""
	b, _ := json.Marshal(copy)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func (s *Server) listAuditEvents(limit int) ([]api.AuditEvent, error) {
	events, err := s.readAuditEvents()
	if err != nil {
		return nil, err
	}
	sort.Slice(events, func(i, j int) bool { return events[i].CreatedAt.After(events[j].CreatedAt) })
	if limit <= 0 {
		limit = 50
	}
	if len(events) > limit {
		events = events[:limit]
	}
	return events, nil
}

func (s *Server) getAuditEvent(id string) (*api.AuditEvent, error) {
	events, err := s.readAuditEvents()
	if err != nil {
		return nil, err
	}
	for i := range events {
		if events[i].ID == id {
			return &events[i], nil
		}
	}
	return nil, fmt.Errorf("no such audit event")
}

func (s *Server) readAuditEvents() ([]api.AuditEvent, error) {
	s.auditMu.Lock()
	defer s.auditMu.Unlock()
	return s.readAuditEventsLocked()
}

func (s *Server) readAuditEventsLocked() ([]api.AuditEvent, error) {
	f, err := os.Open(s.auditPath())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()
	var events []api.AuditEvent
	scanner := bufio.NewScanner(f)
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1024*1024)
	prev := ""
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var event api.AuditEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			return nil, fmt.Errorf("audit chain contains malformed event: %w", err)
		}
		if event.Hash != hashAuditEvent(&event) {
			return nil, fmt.Errorf("audit chain hash mismatch at %s", event.ID)
		}
		if event.PreviousHash != prev {
			return nil, fmt.Errorf("audit chain previous hash mismatch at %s", event.ID)
		}
		prev = event.Hash
		events = append(events, event)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return events, nil
}

func redactAuditText(s string) string {
	if s == "" {
		return ""
	}
	s = auditDSNRE.ReplaceAllString(s, "<redacted-dsn>")
	s = auditSecretRE.ReplaceAllString(s, "$1=<redacted>")
	s = auditAllocIDRE.ReplaceAllString(s, "<redacted-id>")
	s = auditHexIDRE.ReplaceAllString(s, "<redacted-id>")
	return s
}

type auditResponseWriter struct {
	http.ResponseWriter
	status int
}

func (w *auditResponseWriter) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *auditResponseWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = 200
	}
	return w.ResponseWriter.Write(b)
}
