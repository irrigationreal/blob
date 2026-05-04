// Package server: GitHub webhook receiver for preview environments
// (v0.13).
//
// POST /v1/webhooks/github is the only HTTP route in blobd that is NOT
// protected by the bearer-token middleware (see withAuth). HMAC-SHA256
// over the request body, validated against a per-app secret stored on
// disk, IS the auth. Spoofing requires either the bearer token (in
// which case you already have full control) or the per-app secret.
//
// State: /srv/blob/webhooks/<app>/github.json mode 0600. One secret per
// app per provider — currently only "github". Adding gitlab/bitbucket
// later means a sibling file per provider.
//
// Dispatch:
//   pull_request.opened       → createPreview(app, "pr-<N>")
//   pull_request.synchronize  → createPreview(app, "pr-<N>")  (idempotent)
//   pull_request.closed       → destroyPreview(app, "pr-<N>")
// Other events return 202 with a "no-op" hint and don't touch state.
package server

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/darvell/blob/internal/api"
)

const githubProvider = "github"

func (s *Server) webhooksDir(app string) string {
	return filepath.Join(s.cfg.StateDir, "webhooks", app)
}

func (s *Server) webhookPath(app, provider string) string {
	return filepath.Join(s.webhooksDir(app), provider+".json")
}

type webhookSecret struct {
	App      string `json:"app"`
	Provider string `json:"provider"`
	Secret   string `json:"secret"`
}

func (s *Server) loadWebhookSecret(app, provider string) (*webhookSecret, error) {
	b, err := os.ReadFile(s.webhookPath(app, provider))
	if err != nil {
		return nil, err
	}
	w := &webhookSecret{}
	if err := json.Unmarshal(b, w); err != nil {
		return nil, err
	}
	return w, nil
}

func (s *Server) saveWebhookSecret(w *webhookSecret) error {
	if err := os.MkdirAll(s.webhooksDir(w.App), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(w, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.webhookPath(w.App, w.Provider), b, 0o600)
}

// allWebhookSecretsForProvider returns every persisted webhook secret
// across all apps for a given provider. We need this on receive because
// the inbound payload doesn't tell us which app it's for — we identify
// the app by which stored secret successfully validates the HMAC.
func (s *Server) allWebhookSecretsForProvider(provider string) ([]*webhookSecret, error) {
	root := filepath.Join(s.cfg.StateDir, "webhooks")
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []*webhookSecret
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		w, err := s.loadWebhookSecret(e.Name(), provider)
		if err != nil {
			continue
		}
		out = append(out, w)
	}
	return out, nil
}

// --- HTTP: webhook setup (auth-gated) ---------------------------------------

func (s *Server) handleWebhookSetup(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/v1/webhooks/setup/")
	parts := strings.SplitN(rest, "/", 2)
	if len(parts) != 2 {
		writeErr(w, 400, "usage: PUT /v1/webhooks/setup/<provider>/<app>")
		return
	}
	provider, app := parts[0], parts[1]
	if provider != githubProvider {
		writeErr(w, 400, "unsupported provider; only 'github' for now")
		return
	}
	if !validName(app) {
		writeErr(w, 400, "invalid app")
		return
	}
	switch r.Method {
	case "PUT", "POST":
		secret, err := generateWebhookSecret()
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		ws := &webhookSecret{App: app, Provider: provider, Secret: secret}
		if err := s.saveWebhookSecret(ws); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		// Public-facing URL for github to POST to. The base-domain we
		// know is the apex; blobd's API is reachable at
		// blob.<base-domain> (per host-setup.md step 4).
		url := fmt.Sprintf("https://blob.%s/v1/webhooks/github", s.cfg.BaseDomain)
		writeJSON(w, 200, api.WebhookSetupResponse{
			App:    app,
			URL:    url,
			Secret: secret,
		})
	case "GET":
		ws, err := s.loadWebhookSecret(app, provider)
		if err != nil {
			writeErr(w, 404, "no webhook configured for "+app)
			return
		}
		writeJSON(w, 200, api.WebhookSetupResponse{
			App:    app,
			URL:    fmt.Sprintf("https://blob.%s/v1/webhooks/github", s.cfg.BaseDomain),
			Secret: ws.Secret,
		})
	case "DELETE":
		_ = removeIgnoringMissing(s.webhookPath(app, provider))
		writeJSON(w, 200, map[string]any{"app": app, "provider": provider, "removed": true})
	default:
		writeErr(w, 405, "method not allowed")
	}
}

func generateWebhookSecret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// --- HTTP: webhook receive (PUBLIC, hmac-authed) ----------------------------

// handleWebhookGitHub validates X-Hub-Signature-256 against EVERY
// configured app secret in turn. Whichever app's secret produces a
// matching MAC is the app the event applies to. This avoids the need
// to encode app identity in the URL — GitHub's webhook config is
// per-repo, and one repo maps to one app via the stored secret.
//
// This handler is the only public-facing route in blobd; the HMAC is
// the auth. We never log the body or the signature.
func (s *Server) handleWebhookGitHub(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		writeErr(w, 405, "method not allowed")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 4<<20)) // 4 MiB
	if err != nil {
		writeErr(w, 400, "read body: "+err.Error())
		return
	}
	defer r.Body.Close()

	sig := r.Header.Get("X-Hub-Signature-256")
	if !strings.HasPrefix(sig, "sha256=") {
		writeErr(w, 400, "missing or malformed X-Hub-Signature-256")
		return
	}
	provided, err := hex.DecodeString(strings.TrimPrefix(sig, "sha256="))
	if err != nil {
		writeErr(w, 400, "invalid signature hex")
		return
	}

	secrets, err := s.allWebhookSecretsForProvider(githubProvider)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if len(secrets) == 0 {
		writeErr(w, 404, "no webhooks configured")
		return
	}
	var matched *webhookSecret
	for _, ws := range secrets {
		mac := hmac.New(sha256.New, []byte(ws.Secret))
		mac.Write(body)
		if hmac.Equal(mac.Sum(nil), provided) {
			matched = ws
			break
		}
	}
	if matched == nil {
		// Constant-ish-time wrong — no app got a match. Don't tell the
		// caller which app failed; a successful spoofer learns nothing.
		writeErr(w, 401, "signature mismatch")
		return
	}

	event := r.Header.Get("X-GitHub-Event")
	if event != "pull_request" {
		// Other events (push, ping, issues, ...) — accepted but unhandled.
		writeJSON(w, 202, map[string]any{
			"app":   matched.App,
			"event": event,
			"noop":  true,
		})
		return
	}

	var pr githubPullRequestEvent
	if err := json.Unmarshal(body, &pr); err != nil {
		writeErr(w, 400, "decode pull_request payload: "+err.Error())
		return
	}
	if pr.Number <= 0 {
		writeErr(w, 400, "pull_request payload missing number")
		return
	}
	branch := fmt.Sprintf("pr-%d", pr.Number)

	switch pr.Action {
	case "opened", "reopened", "synchronize":
		ctx := context.Background()
		// createPreview is idempotent at the disk-state level (sentinel
		// gets overwritten) but the underlying deploy will rebuild +
		// reschedule. That's the right semantic for synchronize.
		p, err := s.createPreview(ctx, matched.App, branch)
		if err != nil {
			stdLog("webhook github: createPreview %s/%s failed: %v", matched.App, branch, err)
			writeErr(w, 500, err.Error())
			return
		}
		stdLog("webhook github: created preview %s/%s (%s)", matched.App, branch, p.URL)
		writeJSON(w, 200, map[string]any{
			"app":    matched.App,
			"branch": branch,
			"action": pr.Action,
			"url":    p.URL,
		})
	case "closed":
		ctx := context.Background()
		if err := s.destroyPreview(ctx, matched.App, branch); err != nil {
			stdLog("webhook github: destroyPreview %s/%s failed: %v", matched.App, branch, err)
			writeErr(w, 500, err.Error())
			return
		}
		stdLog("webhook github: destroyed preview %s/%s", matched.App, branch)
		writeJSON(w, 200, map[string]any{
			"app":    matched.App,
			"branch": branch,
			"action": pr.Action,
		})
	default:
		// Other PR actions (assigned, review_requested, labeled, ...) —
		// don't touch state.
		writeJSON(w, 202, map[string]any{
			"app":    matched.App,
			"branch": branch,
			"action": pr.Action,
			"noop":   true,
		})
	}
}

// githubPullRequestEvent is the minimal slice of GitHub's pull_request
// event payload we look at. We deliberately don't try to model the full
// shape — adding fields here as we need them is cheaper than a fragile
// 200-line struct.
type githubPullRequestEvent struct {
	Action string `json:"action"`
	Number int    `json:"number"`
}

// removeIgnoringMissing is shared with other modules that want
// idempotent file deletion. Defined elsewhere in this package; this
// comment just records the contract: returns nil when the path is
// already gone.
var _ = errors.New // keep errors imported even though not used directly here