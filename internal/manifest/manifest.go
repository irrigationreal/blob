// Package manifest defines the blob.yaml authoring format.
package manifest

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// Manifest is the v1 blob.yaml shape. It is intentionally small.
type Manifest struct {
	Name     string            `yaml:"name"`
	Form     string            `yaml:"form,omitempty"` // web-service | daemon | cronjob | job | function | vm
	Domain   string            `yaml:"domain,omitempty"`
	Port     int               `yaml:"port,omitempty"`
	Image    string            `yaml:"image,omitempty"`
	CPU      int               `yaml:"cpu,omitempty"`
	Memory   int               `yaml:"memory,omitempty"`
	Replicas int               `yaml:"replicas,omitempty"`
	Env      map[string]string `yaml:"env,omitempty"`
	Schedule string            `yaml:"schedule,omitempty"` // cron expression for cronjob form
}

var nameRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// Load reads blob.yaml from a path. If the file is missing it returns
// an empty Manifest with ErrNotFound so callers can fall back to defaults.
func Load(path string) (*Manifest, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	m := &Manifest{}
	if err := yaml.Unmarshal(b, m); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	m.applyDefaults()
	return m, nil
}

func (m *Manifest) applyDefaults() {
	if m.Form == "" {
		m.Form = "web-service"
	}
	if m.CPU == 0 {
		m.CPU = 500
	}
	if m.Memory == 0 {
		m.Memory = 512
	}
	if m.Replicas == 0 {
		m.Replicas = 1
	}
	m.Name = strings.ToLower(strings.TrimSpace(m.Name))
}

// Validate returns an error if the manifest is unusable.
func (m *Manifest) Validate() error {
	if m.Name == "" {
		return fmt.Errorf("manifest: name is required")
	}
	if !nameRE.MatchString(m.Name) {
		return fmt.Errorf("manifest: invalid name %q (must be lowercase, digits, hyphens; start and end alphanumeric)", m.Name)
	}
	switch m.Form {
	case "web-service", "daemon", "job", "cronjob":
	case "function", "vm":
		return fmt.Errorf("manifest: form %q is reserved but not yet implemented in v1", m.Form)
	default:
		return fmt.Errorf("manifest: unknown form %q", m.Form)
	}
	if m.Form == "web-service" && m.Port <= 0 {
		// Port can be inferred at deploy time from compose/EXPOSE; allowed to be empty.
	}
	if m.Form == "cronjob" && m.Schedule == "" {
		return fmt.Errorf("manifest: cronjob requires schedule")
	}
	return nil
}

// Marshal renders the manifest as YAML (used by `blob init`).
func (m *Manifest) Marshal() ([]byte, error) {
	return yaml.Marshal(m)
}
