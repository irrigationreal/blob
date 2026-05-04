// Package manifest defines the blob.yaml authoring format.
package manifest

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// Component is a single workload that can stand on its own. A top-level
// Manifest is itself a component plus app-level orchestration (Components).
type Component struct {
	Name        string            `yaml:"name,omitempty"`     // optional in single-component manifests; required when used inside Components
	Form        string            `yaml:"form,omitempty"`     // web-service | daemon | job | cronjob | static
	Domain      string            `yaml:"domain,omitempty"`   // overrides default <name>.<base>
	Domains     []string          `yaml:"domains,omitempty"`  // additional hostnames
	Port        int               `yaml:"port,omitempty"`     // required for web-service unless inferable
	Image       string            `yaml:"image,omitempty"`    // if set, deploy registry image directly
	Command     []string          `yaml:"command,omitempty"`  // override the image's entrypoint command
	CPU         int               `yaml:"cpu,omitempty"`      // MHz
	Memory      int               `yaml:"memory,omitempty"`   // MiB
	Replicas    int               `yaml:"replicas,omitempty"` // count
	Env         map[string]string `yaml:"env,omitempty"`      // literal env vars
	Secrets     []SecretRef       `yaml:"secrets,omitempty"`  // env vars sourced from the secret store
	Services    []string          `yaml:"services,omitempty"` // managed services to bind (Postgres etc.); injects DATABASE_URL etc.
	Schedule    string            `yaml:"schedule,omitempty"` // cron expression for cronjob form
	Volumes     []VolumeMount     `yaml:"volumes,omitempty"`  // per-app Docker named volumes
	Sidecars    []Sidecar         `yaml:"sidecars,omitempty"` // additional containers in the same Nomad group (Bundle)

	// Static-site fields (form: static)
	Root        string   `yaml:"root,omitempty"`        // directory inside the source tree to serve (default: ".")
	Build       string   `yaml:"build,omitempty"`       // optional build command (e.g. "npm run build"); output should land in `root` or `dist`
	Index       string   `yaml:"index,omitempty"`       // index file (default: index.html)
	NotFound    string   `yaml:"not_found,omitempty"`   // 404 path (e.g. /404.html); also used for SPA fallback when SPA is true
	SPA         bool     `yaml:"spa,omitempty"`         // if true, serve index.html for any unmatched path (SPA routing)

	// Static is a shorthand for `form: static, root: <dir>`. When set,
	// it implies form: static unless the operator overrode form. Useful
	// for the common case `static: dist` or `static: .`. Equivalent to
	// writing `form: static / root: dist` longhand.
	Static string `yaml:"static,omitempty"`
}

// Sidecar is a co-scheduled container in the same Nomad group.
// Shares the network namespace with the primary task.
type Sidecar struct {
	Name   string            `yaml:"name"`
	Image  string            `yaml:"image"`
	CPU    int               `yaml:"cpu,omitempty"`
	Memory int               `yaml:"memory,omitempty"`
	Env    map[string]string `yaml:"env,omitempty"`
	Args   []string          `yaml:"args,omitempty"`
}

// VolumeMount declares a Docker named volume mounted into the workload.
// Persists across redeploys; managed by `blob volume ...`.
type VolumeMount struct {
	Name string `yaml:"name"` // logical name (a per-app volume; the actual Docker volume is blob-<app>-<name>)
	Path string `yaml:"path"` // mount path inside the container
}

// SecretRef binds a stored secret (by name) to an env var.
// Values are stored encrypted in the blobd state directory and injected
// into the container's environment at deploy time.
type SecretRef struct {
	Env  string `yaml:"env"`  // env var name inside the container
	Name string `yaml:"name"` // secret name in the store (per-environment scope)
}

// Manifest represents a blob.yaml. A manifest can be either:
//   - a single Component (top-level Name + Form, no Components list), or
//   - an App (top-level Name + Components list).
type Manifest struct {
	Component   `yaml:",inline"`         // single-component shorthand
	Environment string      `yaml:"environment,omitempty"` // prod | staging | pr-NNN ...
	Components  []Component `yaml:"components,omitempty"`  // multi-component App
}

var nameRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// Load reads blob.yaml from a path.
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
	if m.Environment == "" {
		m.Environment = "prod"
	}
	m.Component.applyDefaults()
	for i := range m.Components {
		m.Components[i].applyDefaults()
	}
	m.Name = strings.ToLower(strings.TrimSpace(m.Name))
}

func (c *Component) applyDefaults() {
	// `static: <dir>` shorthand → form: static, root: <dir>. Applied
	// before the form default so it doesn't get clobbered. The operator
	// can still override either field longhand if they want.
	if c.Static != "" {
		if c.Form == "" {
			c.Form = "static"
		}
		if c.Root == "" {
			c.Root = c.Static
		}
	}
	if c.Form == "" {
		c.Form = "web-service"
	}
	if c.CPU == 0 {
		c.CPU = 500
	}
	if c.Memory == 0 {
		c.Memory = 512
	}
	if c.Replicas == 0 {
		c.Replicas = 1
	}
	c.Name = strings.ToLower(strings.TrimSpace(c.Name))
}

// IsApp returns true if this manifest declares multiple components.
func (m *Manifest) IsApp() bool { return len(m.Components) > 0 }

// Validate returns an error if the manifest is unusable.
func (m *Manifest) Validate() error {
	if m.Name == "" {
		return fmt.Errorf("manifest: name is required")
	}
	if !nameRE.MatchString(m.Name) {
		return fmt.Errorf("manifest: invalid name %q (lowercase a-z 0-9 hyphens)", m.Name)
	}
	if m.Environment != "" && !envRE.MatchString(m.Environment) {
		return fmt.Errorf("manifest: invalid environment %q", m.Environment)
	}
	if m.IsApp() {
		// In app form, top-level form/port/etc. are ignored. Components are validated.
		seen := map[string]bool{}
		for i, c := range m.Components {
			if c.Name == "" {
				return fmt.Errorf("components[%d]: name is required", i)
			}
			if seen[c.Name] {
				return fmt.Errorf("components: duplicate component name %q", c.Name)
			}
			seen[c.Name] = true
			if err := c.validate(); err != nil {
				return fmt.Errorf("components[%s]: %w", c.Name, err)
			}
		}
		return nil
	}
	return m.Component.validate()
}

var envRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,30}[a-z0-9])?$`)

func (c *Component) validate() error {
	switch c.Form {
	case "web-service", "daemon", "job", "cronjob", "static":
	case "function", "vm":
		return fmt.Errorf("form %q is reserved but not yet implemented", c.Form)
	default:
		return fmt.Errorf("unknown form %q", c.Form)
	}
	if c.Form == "cronjob" && c.Schedule == "" {
		return fmt.Errorf("cronjob requires schedule")
	}
	for _, s := range c.Secrets {
		if s.Env == "" || s.Name == "" {
			return fmt.Errorf("secret ref needs both env and name")
		}
		if !nameRE.MatchString(s.Name) {
			return fmt.Errorf("invalid secret name %q", s.Name)
		}
	}
	for _, v := range c.Volumes {
		if v.Name == "" || v.Path == "" {
			return fmt.Errorf("volume needs both name and path")
		}
		if !nameRE.MatchString(v.Name) {
			return fmt.Errorf("invalid volume name %q", v.Name)
		}
	}
	return nil
}

// Marshal renders the manifest as YAML (used by `blob init`).
func (m *Manifest) Marshal() ([]byte, error) {
	return yaml.Marshal(m)
}
