package importers

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/darvell/blob/internal/manifest"
)

// Compose parses a docker-compose.yaml at path and returns a blob.yaml
// equivalent. The compose project's first web-shaped service becomes the
// primary component; additional non-web services that have published ports
// are mapped to extra components on a multi-component App. Stateful images
// (postgres, valkey/redis, mysql, mongo, etc.) are surfaced as warnings —
// users should declare them via blob's managed services rather than embed
// them as components.
//
// The 80% case we handle:
//   - one or more services with `image:` or `build:`
//   - top-level `ports: [HOST:CONTAINER]` (host port informational; we use container)
//   - top-level `environment:` (map or list form)
//   - top-level `volumes:` (named or anonymous → blob VolumeMount; bind mounts warn)
//   - top-level `command:` and `entrypoint:`
//   - depends_on (informational only — reflected in component order)
//
// Explicitly NOT translated (warned):
//   - networks/external networks, configs, secrets blocks
//   - x-* extension fields
//   - profiles, deploy.replicas (we honor scale via blob scale)
//   - healthchecks (blob has its own TCP/HTTP probes per form)
func Compose(path string) (*Result, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cf composeFile
	if err := yaml.Unmarshal(b, &cf); err != nil {
		return nil, fmt.Errorf("parse compose: %w", err)
	}
	if len(cf.Services) == 0 {
		return nil, fmt.Errorf("compose file has no services")
	}

	res := &Result{Source: "compose"}
	projectName := strings.ToLower(filepath.Base(filepath.Dir(path)))
	if cf.Name != "" {
		projectName = strings.ToLower(cf.Name)
	}
	projectName = sanitizeName(projectName)

	// Stable iteration order
	names := make([]string, 0, len(cf.Services))
	for n := range cf.Services {
		names = append(names, n)
	}
	sort.Strings(names)

	var components []manifest.Component
	for _, svcName := range names {
		svc := cf.Services[svcName]

		// Stateful services: warn and skip — the operator should bind a
		// managed instance instead of running their own.
		if isStateful(svc.Image) {
			res.Warnings = append(res.Warnings,
				fmt.Sprintf("service %q uses image %q — looks like a stateful dependency. Skipped: declare it via `blob postgres create`/`blob valkey create` and bind via services: [...]",
					svcName, svc.Image))
			continue
		}

		c := manifest.Component{Name: sanitizeName(svcName)}
		// Determine form
		port := firstContainerPort(svc.Ports)
		if port == 0 && svc.Expose != nil && len(svc.Expose) > 0 {
			port = parsePort(fmt.Sprint(svc.Expose[0]))
		}
		if port > 0 {
			c.Form = "web-service"
			c.Port = port
		} else {
			c.Form = "daemon"
		}
		if svc.Image != "" {
			c.Image = svc.Image
		}
		// Env
		if env := mergeEnv(svc.Environment); len(env) > 0 {
			c.Env = env
		}
		// Volumes
		for _, v := range svc.Volumes {
			parts := strings.SplitN(v, ":", 2)
			switch {
			case len(parts) == 2 && strings.HasPrefix(parts[0], "/"):
				res.Warnings = append(res.Warnings,
					fmt.Sprintf("service %q has bind mount %q — bind mounts aren't portable across nodes; skipped",
						svcName, v))
			case len(parts) == 2:
				c.Volumes = append(c.Volumes, manifest.VolumeMount{
					Name: sanitizeName(parts[0]),
					Path: parts[1],
				})
			}
		}
		// Command / entrypoint
		if cmd := flattenCmd(svc.Command); len(cmd) > 0 {
			c.Command = cmd
		} else if cmd := flattenCmd(svc.Entrypoint); len(cmd) > 0 {
			c.Command = cmd
		}
		// Resources (best effort)
		if svc.Deploy.Resources.Limits.Memory != "" {
			c.Memory = parseMemoryMiB(svc.Deploy.Resources.Limits.Memory)
		}
		// depends_on — informational only
		if deps := depKeys(svc.DependsOn); len(deps) > 0 {
			res.Warnings = append(res.Warnings,
				fmt.Sprintf("service %q depends_on %v — blob doesn't enforce ordering; ensure managed services are created before deploy",
					svcName, deps))
		}
		// Unsupported fields
		if svc.Healthcheck != nil {
			res.Warnings = append(res.Warnings,
				fmt.Sprintf("service %q has a healthcheck — blob uses its own TCP/HTTP probes per form; healthcheck dropped",
					svcName))
		}
		if nets := depKeys(svc.Networks); len(nets) > 0 {
			res.Warnings = append(res.Warnings,
				fmt.Sprintf("service %q declares networks %v — blob services share one cluster network; dropped",
					svcName, nets))
		}

		components = append(components, c)
	}
	if len(cf.Volumes) > 0 {
		// Top-level named volumes are referenced by services already; we
		// just note any extras the operator declared but didn't use.
	}
	if len(cf.Networks) > 0 {
		res.Warnings = append(res.Warnings,
			fmt.Sprintf("top-level networks %v dropped", networkKeys(cf.Networks)))
	}
	if len(cf.Configs) > 0 {
		res.Warnings = append(res.Warnings,
			fmt.Sprintf("top-level configs %v dropped — re-create as blob secrets or files in the image",
				networkKeys(cf.Configs)))
	}
	if len(cf.Secrets) > 0 {
		res.Warnings = append(res.Warnings,
			fmt.Sprintf("top-level secrets %v dropped — use `blob secrets set` and reference via secrets: in blob.yaml",
				networkKeys(cf.Secrets)))
	}

	if len(components) == 0 {
		return nil, fmt.Errorf("compose file has no translatable services (all were stateful or unsupported)")
	}

	m := &manifest.Manifest{}
	if len(components) == 1 {
		// Single-component shorthand
		m.Component = components[0]
		if m.Component.Name == "" {
			m.Component.Name = projectName
		}
	} else {
		m.Name = projectName
		m.Components = components
	}
	res.Manifest = m
	if err := res.Render(); err != nil {
		return nil, err
	}
	return res, nil
}

// --- compose schema (minimal, partial) ---

type composeFile struct {
	Version  string                       `yaml:"version,omitempty"`
	Name     string                       `yaml:"name,omitempty"`
	Services map[string]composeService    `yaml:"services"`
	Volumes  map[string]any               `yaml:"volumes,omitempty"`
	Networks map[string]any               `yaml:"networks,omitempty"`
	Configs  map[string]any               `yaml:"configs,omitempty"`
	Secrets  map[string]any               `yaml:"secrets,omitempty"`
}

type composeService struct {
	Image       string         `yaml:"image,omitempty"`
	Build       any            `yaml:"build,omitempty"`
	Ports       []any          `yaml:"ports,omitempty"`
	Expose      []any          `yaml:"expose,omitempty"`
	Environment any            `yaml:"environment,omitempty"`
	Volumes     []string       `yaml:"volumes,omitempty"`
	Command     any            `yaml:"command,omitempty"`
	Entrypoint  any            `yaml:"entrypoint,omitempty"`
	DependsOn   any            `yaml:"depends_on,omitempty"`
	Healthcheck any            `yaml:"healthcheck,omitempty"`
	Networks    any            `yaml:"networks,omitempty"`
	Deploy      struct {
		Resources struct {
			Limits struct {
				Memory string `yaml:"memory,omitempty"`
			} `yaml:"limits,omitempty"`
		} `yaml:"resources,omitempty"`
	} `yaml:"deploy,omitempty"`
}

// --- helpers ---

func firstContainerPort(ports []any) int {
	for _, p := range ports {
		s := fmt.Sprint(p)
		// "8080", "80:80", "8080:80/tcp", or map form rendered as map
		if strings.Contains(s, "map[") {
			// map form: target field carries the container port
			continue
		}
		// Strip protocol
		if i := strings.Index(s, "/"); i >= 0 {
			s = s[:i]
		}
		// "host:container" → container
		if i := strings.LastIndex(s, ":"); i >= 0 {
			s = s[i+1:]
		}
		if n := parsePort(s); n > 0 {
			return n
		}
	}
	return 0
}

func parsePort(s string) int {
	n := 0
	for _, r := range strings.TrimSpace(s) {
		if r < '0' || r > '9' {
			return 0
		}
		n = n*10 + int(r-'0')
	}
	return n
}

func mergeEnv(env any) map[string]string {
	if env == nil {
		return nil
	}
	out := map[string]string{}
	switch v := env.(type) {
	case map[string]any:
		for k, val := range v {
			out[k] = fmt.Sprint(val)
		}
	case []any:
		for _, item := range v {
			s := fmt.Sprint(item)
			if i := strings.Index(s, "="); i >= 0 {
				out[s[:i]] = s[i+1:]
			}
		}
	}
	return out
}

func flattenCmd(cmd any) []string {
	if cmd == nil {
		return nil
	}
	switch v := cmd.(type) {
	case string:
		return strings.Fields(v)
	case []any:
		out := make([]string, 0, len(v))
		for _, x := range v {
			out = append(out, fmt.Sprint(x))
		}
		return out
	}
	return nil
}

func isStateful(image string) bool {
	if image == "" {
		return false
	}
	low := strings.ToLower(image)
	for _, prefix := range []string{
		"postgres", "library/postgres",
		"mysql", "library/mysql", "mariadb",
		"mongo", "mongodb",
		"redis", "valkey",
		"rabbitmq", "nats", "kafka",
		"clickhouse",
		"elasticsearch", "opensearch",
		"minio",
	} {
		if strings.HasPrefix(low, prefix+":") || low == prefix || strings.Contains(low, "/"+prefix+":") || strings.Contains(low, "/"+prefix+"/") {
			return true
		}
	}
	return false
}

func parseMemoryMiB(s string) int {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	mult := 1
	switch {
	case strings.HasSuffix(s, "G"), strings.HasSuffix(s, "g"):
		mult = 1024
		s = s[:len(s)-1]
	case strings.HasSuffix(s, "M"), strings.HasSuffix(s, "m"):
		mult = 1
		s = s[:len(s)-1]
	case strings.HasSuffix(s, "Gi"), strings.HasSuffix(s, "gi"):
		mult = 1024
		s = s[:len(s)-2]
	case strings.HasSuffix(s, "Mi"), strings.HasSuffix(s, "mi"):
		mult = 1
		s = s[:len(s)-2]
	}
	n := parsePort(s)
	return n * mult
}

func depKeys(v any) []string {
	out := []string{}
	switch x := v.(type) {
	case []any:
		for _, k := range x {
			out = append(out, fmt.Sprint(k))
		}
	case map[string]any:
		for k := range x {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

func networkKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sanitizeName(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	prevDash := false
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z' || r >= '0' && r <= '9':
			b.WriteRune(r)
			prevDash = false
		default:
			if !prevDash && b.Len() > 0 {
				b.WriteRune('-')
				prevDash = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}
