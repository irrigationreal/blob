// Package server: custom-domain TLS bindings (v0.22).
//
// Today's flow without certs.go: the operator calls
// `blob domains add <app> <host>` and Traefik's `le` http-01 resolver
// quietly picks up the new Host(...) router rule and provisions a
// Let's Encrypt cert. That works, but there's no surface to *list*
// what's been bound, *check* whether the cert actually landed, or
// *remove* a binding cleanly. v0.22 fills that gap.
//
// Per-binding meta lives at <StateDir>/certs/<hostname>.json. Adding
// a binding calls into the existing attachDomain plumbing (which
// re-renders the Nomad job with the new Host in the Traefik tags) and
// then probes the live edge for the issued cert. Verification is
// idempotent: re-running it just refreshes Verified/LastIssuer.
package server

import (
	"context"
	"crypto/tls"
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

func (s *Server) certsDir() string {
	return filepath.Join(s.cfg.StateDir, "certs")
}

func certFileName(host string) string {
	// Hostnames are case-insensitive; persist lowercased to keep the
	// file path canonical.
	return strings.ToLower(host) + ".json"
}

func (s *Server) loadCert(host string) (*api.CertBinding, error) {
	b, err := os.ReadFile(filepath.Join(s.certsDir(), certFileName(host)))
	if err != nil {
		return nil, err
	}
	c := &api.CertBinding{}
	if err := json.Unmarshal(b, c); err != nil {
		return nil, err
	}
	return c, nil
}

func (s *Server) saveCert(c *api.CertBinding) error {
	if err := os.MkdirAll(s.certsDir(), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(s.certsDir(), certFileName(c.Hostname)), b, 0o600)
}

func (s *Server) deleteCert(host string) error {
	p := filepath.Join(s.certsDir(), certFileName(host))
	if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// --- HTTP handlers -----------------------------------------------------------

func (s *Server) handleCerts(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "GET":
		out, err := s.listCerts(r.Context())
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, out)
	case "POST":
		var req api.AddCertRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		out, err := s.addCert(r.Context(), &req)
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, out)
	default:
		writeErr(w, 405, "method not allowed")
	}
}

func (s *Server) handleCertsItem(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/v1/certs/")
	parts := strings.SplitN(rest, "/", 2)
	host := strings.ToLower(parts[0])
	if !validHostname(host) {
		writeErr(w, 400, "invalid hostname")
		return
	}
	switch {
	case len(parts) == 1 && r.Method == "GET":
		c, err := s.loadCert(host)
		if err != nil {
			writeErr(w, 404, "no such cert binding")
			return
		}
		writeJSON(w, 200, c)
	case len(parts) == 1 && r.Method == "DELETE":
		if err := s.removeCert(r.Context(), host); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, map[string]any{"hostname": host, "removed": true})
	case len(parts) == 2 && parts[1] == "verify" && (r.Method == "POST" || r.Method == "GET"):
		c, err := s.verifyCert(r.Context(), host)
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, &api.VerifyCertResponse{Binding: *c})
	default:
		writeErr(w, 404, "not found")
	}
}

// --- core flows --------------------------------------------------------------

func (s *Server) addCert(ctx context.Context, req *api.AddCertRequest) (*api.AddCertResponse, error) {
	if req.App == "" || !validHostname(req.Hostname) {
		return nil, errors.New("app and hostname are required; hostname must look like example.com")
	}
	host := strings.ToLower(req.Hostname)
	// Attaching a cert for a host that already lives under the
	// platform base domain is wasted work — the wildcard already
	// covers it.
	if isPlatformBaseHost(host, s.cfg.BaseDomain) {
		return nil, fmt.Errorf("hostname %q is under the platform wildcard *.%s — no separate cert needed", host, s.cfg.BaseDomain)
	}
	if existing, err := s.loadCert(host); err == nil {
		if existing.App != req.App {
			return nil, fmt.Errorf("hostname %q already bound to app %q", host, existing.App)
		}
		// Same binding — refresh state but do not re-attach.
		existing.LastProbe = time.Time{}
		_ = s.saveCert(existing)
		return &api.AddCertResponse{
			Binding: *existing,
			Note:    "binding already existed; run `blob certs verify " + host + "` to re-probe issuance",
		}, nil
	}
	// Hand off to the existing attachDomain plumbing, which re-renders
	// the running Nomad job with the new Host in the Traefik rule and
	// reuses the `le` certresolver wired in by bootstrap-host.sh.
	att, err := s.attachDomain(ctx, &api.DomainAttachRequest{
		App:  req.App,
		Host: host,
		Mode: "user-external",
	})
	if err != nil {
		return nil, fmt.Errorf("attach domain: %w", err)
	}
	cb := &api.CertBinding{
		App:       req.App,
		Hostname:  host,
		CreatedAt: time.Now().UTC(),
	}
	if err := s.saveCert(cb); err != nil {
		return nil, err
	}
	return &api.AddCertResponse{
		Binding:    *cb,
		DNSRecords: att.DNSRecords,
		Note:       "Traefik will request a Let's Encrypt cert via http-01 on the next request to https://" + host + " — point an A record at the platform IP if you haven't, then run `blob certs verify " + host + "`.",
	}, nil
}

func (s *Server) removeCert(ctx context.Context, host string) error {
	cb, err := s.loadCert(host)
	if err != nil {
		return errors.New("no such cert binding")
	}
	id := cb.App
	jobPath := joinPath(s.cfg.JobsDir, id+".nomad")
	if existing, err := readFile(jobPath); err == nil {
		updated, err := removeHostFromTraefikRule(existing, host)
		if err == nil && updated != existing {
			if err := writeFileAtomic(jobPath, []byte(updated)); err != nil {
				return err
			}
			if err := s.run(ctx, "nomad", "job", "run", jobPath); err != nil {
				return err
			}
		}
	}
	return s.deleteCert(host)
}

func (s *Server) listCerts(ctx context.Context) (*api.ListCertsResponse, error) {
	out := &api.ListCertsResponse{}
	entries, err := os.ReadDir(s.certsDir())
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
		host := strings.TrimSuffix(e.Name(), ".json")
		c, err := s.loadCert(host)
		if err != nil {
			continue
		}
		out.Certs = append(out.Certs, *c)
	}
	sort.Slice(out.Certs, func(i, j int) bool { return out.Certs[i].Hostname < out.Certs[j].Hostname })
	return out, nil
}

// verifyCert opens a TLS connection to the live edge for the bound
// hostname and inspects the served certificate. Pass when:
// (a) the issuer's CN starts with "(STAGING) " is fine for the staging
//     resolver, or includes "Let's Encrypt" / "E5" / "E6" / "R10"
//     / "R11" / "R12" — the public LE intermediates,
// (b) the leaf's SAN list contains the requested hostname.
//
// Failure modes recorded into LastError so `blob certs list` can show
// the operator what to fix (DNS pending, http-01 still cycling, etc).
func (s *Server) verifyCert(ctx context.Context, host string) (*api.CertBinding, error) {
	cb, err := s.loadCert(host)
	if err != nil {
		return nil, errors.New("no such cert binding")
	}
	cb.LastProbe = time.Now().UTC()
	cb.LastError = ""
	cb.LastIssuer = ""
	cb.Verified = false

	dialer := &tls.Dialer{
		Config: &tls.Config{
			ServerName:         host,
			InsecureSkipVerify: true, // we're inspecting issuer/SAN ourselves
		},
	}
	dctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	conn, err := dialer.DialContext(dctx, "tcp", host+":443")
	if err != nil {
		cb.LastError = fmt.Sprintf("dial %s:443: %v", host, err)
		_ = s.saveCert(cb)
		return cb, nil
	}
	defer conn.Close()
	tconn, ok := conn.(*tls.Conn)
	if !ok {
		cb.LastError = "connection was not TLS"
		_ = s.saveCert(cb)
		return cb, nil
	}
	state := tconn.ConnectionState()
	if len(state.PeerCertificates) == 0 {
		cb.LastError = "edge served no certificates"
		_ = s.saveCert(cb)
		return cb, nil
	}
	leaf := state.PeerCertificates[0]
	cb.LastIssuer = leaf.Issuer.CommonName

	hasSAN := false
	for _, dns := range leaf.DNSNames {
		if strings.EqualFold(dns, host) {
			hasSAN = true
			break
		}
	}
	if !hasSAN {
		cb.LastError = fmt.Sprintf("leaf SAN list %v does not include %q (probably still serving the wildcard fallback)", leaf.DNSNames, host)
		_ = s.saveCert(cb)
		return cb, nil
	}
	if !looksLikeLetsEncrypt(cb.LastIssuer) {
		cb.LastError = fmt.Sprintf("leaf issuer %q is not Let's Encrypt — http-01 likely failed and Traefik is serving its self-signed default", cb.LastIssuer)
		_ = s.saveCert(cb)
		return cb, nil
	}
	cb.Verified = true
	_ = s.saveCert(cb)
	return cb, nil
}

func looksLikeLetsEncrypt(issuer string) bool {
	low := strings.ToLower(issuer)
	switch {
	case strings.Contains(low, "let's encrypt"):
		return true
	case strings.Contains(low, "letsencrypt"):
		return true
	}
	// Public LE intermediates (2024-2026) ship as bare letters: R10,
	// R11, R12, E5, E6, E7. Staging is "(STAGING) Wannabe Watercress
	// R12" etc.
	for _, suf := range []string{"R10", "R11", "R12", "R13", "R14", "E5", "E6", "E7", "E8"} {
		if strings.HasSuffix(issuer, suf) {
			return true
		}
	}
	return false
}

func isPlatformBaseHost(host, baseDomain string) bool {
	if baseDomain == "" {
		return false
	}
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	bd := strings.ToLower(strings.TrimSuffix(baseDomain, "."))
	if host == bd {
		return true
	}
	if !strings.HasSuffix(host, "."+bd) {
		return false
	}
	// Wildcard *.<base> matches a single label only. Multi-label
	// subdomains like tls.demo.<base> still need their own cert.
	rest := strings.TrimSuffix(host, "."+bd)
	return !strings.Contains(rest, ".")
}

// validHostname is intentionally permissive — Traefik / LE will reject
// anything that doesn't actually resolve, so we just guard against
// obviously dangerous input that could break the URL path or rule
// parsing.
func validHostname(h string) bool {
	if h == "" || len(h) > 253 {
		return false
	}
	for _, r := range h {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '-' || r == '.':
		default:
			return false
		}
	}
	if strings.HasPrefix(h, ".") || strings.HasPrefix(h, "-") {
		return false
	}
	return strings.Contains(h, ".")
}

// removeHostFromTraefikRule is the inverse of addHostToTraefikRule.
// It drops every Host(`host`) clause for the given hostname from the
// rendered tags. If the hostname isn't present, the rendered job is
// returned unchanged.
func removeHostFromTraefikRule(rendered, host string) (string, error) {
	hl := strings.ToLower(host)
	out := strings.Builder{}
	out.Grow(len(rendered))
	for _, line := range strings.Split(rendered, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.Contains(trimmed, "traefik.http.routers.") || !strings.Contains(trimmed, ".rule=") {
			out.WriteString(line)
			out.WriteByte('\n')
			continue
		}
		// Pull out the `Host(...)` clauses and drop ours.
		idx := strings.Index(line, ".rule=")
		if idx < 0 {
			out.WriteString(line)
			out.WriteByte('\n')
			continue
		}
		prefix := line[:idx+len(".rule=")]
		// Find the closing quote of the rule value (it's wrapped in `"`).
		rest := line[idx+len(".rule="):]
		// rest looks like: "Host(`a`) || Host(`b`)",
		q1 := strings.Index(rest, "\"")
		if q1 < 0 {
			out.WriteString(line)
			out.WriteByte('\n')
			continue
		}
		q2 := strings.Index(rest[q1+1:], "\"")
		if q2 < 0 {
			out.WriteString(line)
			out.WriteByte('\n')
			continue
		}
		ruleBody := rest[q1+1 : q1+1+q2]
		clauses := strings.Split(ruleBody, " || ")
		kept := clauses[:0]
		for _, c := range clauses {
			c = strings.TrimSpace(c)
			lc := strings.ToLower(c)
			needle := "host(`" + hl + "`)"
			if lc == needle {
				continue
			}
			kept = append(kept, c)
		}
		if len(kept) == 0 {
			// Don't strip the only host — that would orphan the router.
			// Bail and keep the line intact; deleteCert still removes
			// the meta file.
			out.WriteString(line)
			out.WriteByte('\n')
			continue
		}
		newRule := strings.Join(kept, " || ")
		out.WriteString(prefix)
		out.WriteString("\"")
		out.WriteString(newRule)
		out.WriteString(rest[q1+1+q2:])
		out.WriteByte('\n')
	}
	res := out.String()
	// Trailing newline: strings.Split + join above adds one extra; trim
	// to keep the file diff minimal when no change was made.
	res = strings.TrimRight(res, "\n") + "\n"
	return res, nil
}
