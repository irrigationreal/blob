// Package secrets is a small at-rest-encrypted secret store backed by files.
//
// Layout: <root>/<environment>/<name>.enc
// Each file is sealed with AES-256-GCM under a single host key derived from
// /etc/blob/key (created on first use, mode 0600). Environments are isolated
// by directory.
//
// This is the v1 native secret store referenced in the spec. External drivers
// (Vault, AWS Secrets Manager, etc.) plug in behind the same interface later.
package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type Store struct {
	root    string // e.g. /srv/blob/secrets
	keyPath string
	key     []byte
}

func New(root, keyPath string) (*Store, error) {
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, err
	}
	s := &Store{root: root, keyPath: keyPath}
	if err := s.loadOrCreateKey(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) loadOrCreateKey() error {
	if err := os.MkdirAll(filepath.Dir(s.keyPath), 0o700); err != nil {
		return err
	}
	b, err := os.ReadFile(s.keyPath)
	if err == nil {
		raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(b)))
		if err != nil {
			return fmt.Errorf("secret key: %w", err)
		}
		if len(raw) != 32 {
			return errors.New("secret key: must be 32 bytes")
		}
		s.key = raw
		return nil
	}
	if !os.IsNotExist(err) {
		return err
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return err
	}
	if err := os.WriteFile(s.keyPath, []byte(base64.StdEncoding.EncodeToString(raw)+"\n"), 0o600); err != nil {
		return err
	}
	s.key = raw
	return nil
}

type record struct {
	Name      string
	Env       string
	Value     []byte
	UpdatedAt time.Time
	Length    int
}

func (s *Store) seal(plain []byte) ([]byte, error) {
	block, err := aes.NewCipher(s.key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	out := gcm.Seal(nonce, nonce, plain, nil)
	return out, nil
}

func (s *Store) open(sealed []byte) ([]byte, error) {
	block, err := aes.NewCipher(s.key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(sealed) < gcm.NonceSize() {
		return nil, errors.New("ciphertext too short")
	}
	nonce, ct := sealed[:gcm.NonceSize()], sealed[gcm.NonceSize():]
	return gcm.Open(nil, nonce, ct, nil)
}

func (s *Store) path(env, name string) string {
	if env == "" {
		env = "prod"
	}
	return filepath.Join(s.root, env, name+".enc")
}

func (s *Store) Set(env, name, value string) error {
	if env == "" {
		env = "prod"
	}
	dir := filepath.Join(s.root, env)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	sealed, err := s.seal([]byte(value))
	if err != nil {
		return err
	}
	tmp := s.path(env, name) + ".tmp"
	if err := os.WriteFile(tmp, sealed, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path(env, name))
}

// Get returns the plaintext for the named secret. Internal use only.
func (s *Store) Get(env, name string) (string, error) {
	b, err := os.ReadFile(s.path(env, name))
	if err != nil {
		return "", err
	}
	plain, err := s.open(b)
	if err != nil {
		return "", err
	}
	return string(plain), nil
}

func (s *Store) Delete(env, name string) error {
	err := os.Remove(s.path(env, name))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// Meta describes a secret without revealing the value.
type Meta struct {
	Name      string
	Env       string
	UpdatedAt time.Time
	Length    int
}

func (s *Store) Meta(env, name string) (Meta, error) {
	st, err := os.Stat(s.path(env, name))
	if err != nil {
		return Meta{}, err
	}
	plain, err := s.Get(env, name)
	if err != nil {
		return Meta{}, err
	}
	return Meta{Name: name, Env: env, UpdatedAt: st.ModTime(), Length: len(plain)}, nil
}

func (s *Store) List(env string) ([]Meta, error) {
	if env == "" {
		env = "prod"
	}
	dir := filepath.Join(s.root, env)
	ents, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []Meta
	for _, e := range ents {
		if !strings.HasSuffix(e.Name(), ".enc") {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".enc")
		m, err := s.Meta(env, name)
		if err != nil {
			continue
		}
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}
