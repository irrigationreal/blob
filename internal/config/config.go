// Package config loads and saves blobctl client config.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

type Config struct {
	Endpoint string `json:"endpoint"`
	Token    string `json:"token,omitempty"`
}

func path() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "blob", "config.json"), nil
}

func Load() (*Config, error) {
	p, err := path()
	if err != nil {
		return nil, err
	}
	c := &Config{}
	if env := os.Getenv("BLOB_HOST"); env != "" {
		c.Endpoint = env
	}
	if env := os.Getenv("BLOB_TOKEN"); env != "" {
		c.Token = env
	}
	b, err := os.ReadFile(p)
	if err == nil {
		_ = json.Unmarshal(b, c)
		// env overrides file
		if env := os.Getenv("BLOB_HOST"); env != "" {
			c.Endpoint = env
		}
		if env := os.Getenv("BLOB_TOKEN"); env != "" {
			c.Token = env
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	return c, nil
}

func Save(c *Config) error {
	p, err := path()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(p, b, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", p, err)
	}
	return nil
}
