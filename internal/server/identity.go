package server

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/darvell/blob/internal/api"
)

var validScopeRE = regexp.MustCompile(`^(\*|[a-z][a-z0-9-]*(?::[a-z][a-z0-9-]*)?)$`)

type authActor struct {
	ID     string
	Name   string
	Owner  bool
	Scopes []string
}

type authActorKey struct{}

type identityTokenMeta struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	Hash      string     `json:"hash"`
	CreatedAt time.Time  `json:"created_at"`
	RevokedAt *time.Time `json:"revoked_at,omitempty"`
}

type identityGrantSet struct {
	TokenID   string    `json:"token_id"`
	Scopes    []string  `json:"scopes"`
	UpdatedAt time.Time `json:"updated_at"`
}

func actorFromContext(ctx context.Context) authActor {
	if a, ok := ctx.Value(authActorKey{}).(authActor); ok {
		return a
	}
	return authActor{}
}

func (a authActor) hasScope(scope string) bool {
	if a.Owner || scope == "" {
		return true
	}
	for _, s := range a.Scopes {
		if s == "*" || s == scope {
			return true
		}
	}
	return false
}

func (s *Server) identityDir() string {
	return filepath.Join(s.cfg.StateDir, "identity")
}

func (s *Server) identityTokensDir() string {
	return filepath.Join(s.identityDir(), "tokens")
}

func (s *Server) identityGrantsDir() string {
	return filepath.Join(s.identityDir(), "grants")
}

func identityTokenPath(base, id string) string {
	return filepath.Join(base, id+".json")
}

func hashServiceToken(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

func newServiceTokenSecret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "blob_" + base64.RawURLEncoding.EncodeToString(b), nil
}

func newIdentityTokenID() (string, error) {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "tok-" + hex.EncodeToString(b), nil
}

func (s *Server) authenticateBearer(token string) (authActor, bool) {
	if s.cfg.Token != "" && subtle.ConstantTimeCompare([]byte(token), []byte(s.cfg.Token)) == 1 {
		return authActor{ID: "owner", Name: "owner", Owner: true, Scopes: []string{"*"}}, true
	}
	hash := hashServiceToken(token)
	metas, err := s.loadIdentityTokenMetas()
	if err != nil {
		stdLog("identity token load failed: %v", err)
		return authActor{}, false
	}
	for _, meta := range metas {
		if meta.RevokedAt != nil {
			continue
		}
		if subtle.ConstantTimeCompare([]byte(meta.Hash), []byte(hash)) == 1 {
			scopes, _ := s.scopesForToken(meta.ID)
			return authActor{ID: meta.ID, Name: meta.Name, Scopes: scopes}, true
		}
	}
	return authActor{}, false
}

func (s *Server) requiredScope(r *http.Request) string {
	path := r.URL.Path
	method := r.Method
	if path == "/v1/whoami" {
		return ""
	}
	if strings.HasPrefix(path, "/ui") {
		if scope, ok := uiScopeForPath(path); ok {
			return scope
		}
		return "admin:read"
	}
	if strings.HasPrefix(path, "/v1/identity") {
		return "identity:admin"
	}
	if strings.HasPrefix(path, "/v1/audit") {
		return "audit:read"
	}
	if strings.HasPrefix(path, "/v1/secrets") {
		if method == "GET" {
			return "secrets:read"
		}
		return "secrets:write"
	}
	if method == "POST" && (path == "/v1/deploy" || path == "/v1/deploy-image" || path == "/v1/deploy-app" || strings.HasPrefix(path, "/v1/sources/")) {
		return "deploy:write"
	}
	if strings.HasPrefix(path, "/v1/apps") {
		if method == "GET" {
			return "apps:read"
		}
		return "deploy:write"
	}
	if method == "GET" {
		return "admin:read"
	}
	return "admin:write"
}

func (s *Server) handleIdentityRoot(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		writeErr(w, 405, "method not allowed")
		return
	}
	tokens, grants, err := s.identityOverview()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"tokens": tokens.Tokens, "grants": grants.Grants})
}

func (s *Server) handleIdentityTokens(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "GET":
		out, err := s.listIdentityTokens()
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, out)
	case "POST":
		var req api.CreateIdentityTokenRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		out, err := s.createIdentityToken(&req)
		if err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		writeJSON(w, 200, out)
	default:
		writeErr(w, 405, "method not allowed")
	}
}

func (s *Server) handleIdentityTokenItem(w http.ResponseWriter, r *http.Request) {
	id := strings.Trim(strings.TrimPrefix(r.URL.Path, "/v1/identity/tokens/"), "/")
	if !validIdentityTokenID(id) {
		writeErr(w, 400, "invalid token id")
		return
	}
	switch r.Method {
	case "GET":
		meta, err := s.loadIdentityTokenMeta(id)
		if err != nil {
			writeErr(w, 404, "no such token")
			return
		}
		writeJSON(w, 200, publicIdentityToken(meta, nil))
	case "DELETE":
		if err := s.revokeIdentityToken(id); err != nil {
			writeErr(w, 404, err.Error())
			return
		}
		writeJSON(w, 200, map[string]any{"id": id, "revoked": true})
	default:
		writeErr(w, 405, "method not allowed")
	}
}

func (s *Server) handleIdentityGrants(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "GET":
		out, err := s.listIdentityGrants(r.URL.Query().Get("token_id"))
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, out)
	case "POST":
		var req api.IdentityGrantRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		out, err := s.addIdentityGrant(&req)
		if err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		writeJSON(w, 200, out)
	default:
		writeErr(w, 405, "method not allowed")
	}
}

func (s *Server) handleIdentityGrantItem(w http.ResponseWriter, r *http.Request) {
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/v1/identity/grants/"), "/")
	parts := strings.Split(rest, "/")
	if len(parts) != 2 || !validIdentityTokenID(parts[0]) || !validScope(parts[1]) {
		writeErr(w, 400, "usage: /v1/identity/grants/<token-id>/<scope>")
		return
	}
	if r.Method != "DELETE" {
		writeErr(w, 405, "method not allowed")
		return
	}
	if err := s.removeIdentityGrant(parts[0], parts[1]); err != nil {
		writeErr(w, 404, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"token_id": parts[0], "scope": parts[1], "revoked": true})
}

func (s *Server) createIdentityToken(req *api.CreateIdentityTokenRequest) (*api.CreateIdentityTokenResponse, error) {
	name := strings.TrimSpace(req.Name)
	if name == "" || len(name) > 80 {
		return nil, errors.New("name is required and must be <= 80 chars")
	}
	id, err := newIdentityTokenID()
	if err != nil {
		return nil, err
	}
	secret, err := newServiceTokenSecret()
	if err != nil {
		return nil, err
	}
	meta := &identityTokenMeta{ID: id, Name: name, Hash: hashServiceToken(secret), CreatedAt: time.Now().UTC()}
	if err := s.saveIdentityTokenMeta(meta); err != nil {
		return nil, err
	}
	return &api.CreateIdentityTokenResponse{Token: publicIdentityToken(meta, nil), Secret: secret}, nil
}

func (s *Server) listIdentityTokens() (*api.ListIdentityTokensResponse, error) {
	tokens, _, err := s.identityOverview()
	return tokens, err
}

func (s *Server) identityOverview() (*api.ListIdentityTokensResponse, *api.ListIdentityGrantsResponse, error) {
	metas, err := s.loadIdentityTokenMetas()
	if err != nil {
		return nil, nil, err
	}
	sets, err := s.loadAllIdentityGrantSets()
	if err != nil {
		return nil, nil, err
	}
	scopesByToken := map[string][]string{}
	grants := &api.ListIdentityGrantsResponse{}
	for _, set := range sets {
		scopes := append([]string(nil), set.Scopes...)
		sort.Strings(scopes)
		scopesByToken[set.TokenID] = scopes
		for _, scope := range scopes {
			grants.Grants = append(grants.Grants, api.IdentityGrant{TokenID: set.TokenID, Scope: scope, CreatedAt: set.UpdatedAt})
		}
	}
	tokens := &api.ListIdentityTokensResponse{}
	for _, meta := range metas {
		tokens.Tokens = append(tokens.Tokens, publicIdentityToken(&meta, scopesByToken[meta.ID]))
	}
	sort.Slice(tokens.Tokens, func(i, j int) bool { return tokens.Tokens[i].CreatedAt.Before(tokens.Tokens[j].CreatedAt) })
	sortIdentityGrants(grants.Grants)
	return tokens, grants, nil
}

func (s *Server) addIdentityGrant(req *api.IdentityGrantRequest) (*api.IdentityGrant, error) {
	if !validIdentityTokenID(req.TokenID) {
		return nil, errors.New("invalid token id")
	}
	if !validScope(req.Scope) {
		return nil, errors.New("invalid scope")
	}
	if _, err := s.loadIdentityTokenMeta(req.TokenID); err != nil {
		return nil, errors.New("no such token")
	}
	set, err := s.loadIdentityGrantSet(req.TokenID)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	if set == nil {
		set = &identityGrantSet{TokenID: req.TokenID}
	}
	if !hasString(set.Scopes, req.Scope) {
		set.Scopes = append(set.Scopes, req.Scope)
		sort.Strings(set.Scopes)
	}
	set.UpdatedAt = time.Now().UTC()
	if err := s.saveIdentityGrantSet(set); err != nil {
		return nil, err
	}
	return &api.IdentityGrant{TokenID: req.TokenID, Scope: req.Scope, CreatedAt: set.UpdatedAt}, nil
}

func (s *Server) loadAllIdentityGrantSets() ([]*identityGrantSet, error) {
	entries, err := os.ReadDir(s.identityGrantsDir())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	sets := make([]*identityGrantSet, 0, len(entries))
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		set, err := s.loadIdentityGrantSet(strings.TrimSuffix(e.Name(), ".json"))
		if err != nil {
			continue
		}
		sets = append(sets, set)
	}
	return sets, nil
}

func sortIdentityGrants(grants []api.IdentityGrant) {
	sort.Slice(grants, func(i, j int) bool {
		if grants[i].TokenID == grants[j].TokenID {
			return grants[i].Scope < grants[j].Scope
		}
		return grants[i].TokenID < grants[j].TokenID
	})
}

func (s *Server) listIdentityGrants(tokenID string) (*api.ListIdentityGrantsResponse, error) {
	out := &api.ListIdentityGrantsResponse{}
	if tokenID != "" {
		if !validIdentityTokenID(tokenID) {
			return nil, errors.New("invalid token id")
		}
		set, err := s.loadIdentityGrantSet(tokenID)
		if err != nil {
			if os.IsNotExist(err) {
				return out, nil
			}
			return nil, err
		}
		scopes := append([]string(nil), set.Scopes...)
		sort.Strings(scopes)
		for _, scope := range scopes {
			out.Grants = append(out.Grants, api.IdentityGrant{TokenID: tokenID, Scope: scope, CreatedAt: set.UpdatedAt})
		}
		return out, nil
	}
	sets, err := s.loadAllIdentityGrantSets()
	if err != nil {
		return nil, err
	}
	for _, set := range sets {
		scopes := append([]string(nil), set.Scopes...)
		sort.Strings(scopes)
		for _, scope := range scopes {
			out.Grants = append(out.Grants, api.IdentityGrant{TokenID: set.TokenID, Scope: scope, CreatedAt: set.UpdatedAt})
		}
	}
	sortIdentityGrants(out.Grants)
	return out, nil
}

func (s *Server) removeIdentityGrant(tokenID, scope string) error {
	set, err := s.loadIdentityGrantSet(tokenID)
	if err != nil {
		return errors.New("grant not found")
	}
	before := len(set.Scopes)
	filtered := set.Scopes[:0]
	for _, s := range set.Scopes {
		if s != scope {
			filtered = append(filtered, s)
		}
	}
	if len(filtered) == before {
		return errors.New("grant not found")
	}
	set.Scopes = filtered
	set.UpdatedAt = time.Now().UTC()
	if len(set.Scopes) == 0 {
		if err := os.Remove(s.identityGrantPath(tokenID)); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	return s.saveIdentityGrantSet(set)
}

func (s *Server) revokeIdentityToken(id string) error {
	meta, err := s.loadIdentityTokenMeta(id)
	if err != nil {
		return errors.New("no such token")
	}
	now := time.Now().UTC()
	meta.RevokedAt = &now
	return s.saveIdentityTokenMeta(meta)
}

func (s *Server) loadIdentityTokenMetas() ([]identityTokenMeta, error) {
	entries, err := os.ReadDir(s.identityTokensDir())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	out := make([]identityTokenMeta, 0, len(entries))
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		meta, err := s.loadIdentityTokenMeta(strings.TrimSuffix(e.Name(), ".json"))
		if err != nil {
			continue
		}
		out = append(out, *meta)
	}
	return out, nil
}

func (s *Server) loadIdentityTokenMeta(id string) (*identityTokenMeta, error) {
	b, err := os.ReadFile(identityTokenPath(s.identityTokensDir(), id))
	if err != nil {
		return nil, err
	}
	meta := &identityTokenMeta{}
	if err := json.Unmarshal(b, meta); err != nil {
		return nil, err
	}
	return meta, nil
}

func (s *Server) saveIdentityTokenMeta(meta *identityTokenMeta) error {
	if err := os.MkdirAll(s.identityTokensDir(), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(identityTokenPath(s.identityTokensDir(), meta.ID), b, 0o600)
}

func (s *Server) identityGrantPath(tokenID string) string {
	return filepath.Join(s.identityGrantsDir(), tokenID+".json")
}

func (s *Server) loadIdentityGrantSet(tokenID string) (*identityGrantSet, error) {
	b, err := os.ReadFile(s.identityGrantPath(tokenID))
	if err != nil {
		return nil, err
	}
	set := &identityGrantSet{}
	if err := json.Unmarshal(b, set); err != nil {
		return nil, err
	}
	return set, nil
}

func (s *Server) saveIdentityGrantSet(set *identityGrantSet) error {
	if err := os.MkdirAll(s.identityGrantsDir(), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(set, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.identityGrantPath(set.TokenID), b, 0o600)
}

func (s *Server) scopesForToken(tokenID string) ([]string, error) {
	set, err := s.loadIdentityGrantSet(tokenID)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	scopes := append([]string(nil), set.Scopes...)
	sort.Strings(scopes)
	return scopes, nil
}

func publicIdentityToken(meta *identityTokenMeta, scopes []string) api.IdentityToken {
	out := api.IdentityToken{ID: meta.ID, Name: meta.Name, CreatedAt: meta.CreatedAt, Scopes: scopes}
	if meta.RevokedAt != nil {
		out.RevokedAt = *meta.RevokedAt
	}
	return out
}

func validIdentityTokenID(id string) bool {
	return strings.HasPrefix(id, "tok-") && validName(strings.TrimPrefix(id, "tok-"))
}

func validScope(scope string) bool {
	return validScopeRE.MatchString(scope)
}

func hasString(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}
