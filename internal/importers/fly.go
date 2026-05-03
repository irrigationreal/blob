package importers

import (
	"fmt"
	"os"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/darvell/blob/internal/manifest"
)

// Fly parses a fly.toml at path and returns a blob.yaml.
//
// The 80% case:
//   - app, primary_region (informational; we drop region — blob is single-region)
//   - [build] dockerfile (informational; blob auto-detects the Dockerfile)
//   - [env] map → blob env
//   - [http_service] internal_port → blob port; force_https/auto_stop_machines etc. dropped
//   - [[mounts]] source/destination → blob volume
//   - [processes] map → multi-component manifest
//   - [deploy] release_command → warned (similar to Procfile release: blob doesn't auto-run)
//
// Explicitly dropped (warned):
//   - [[services]] block (older fly format) beyond the simplest case
//   - [[vm]] (region-specific machine sizes)
//   - autoscaling, [[checks]], statics
func Fly(path string) (*Result, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var ft flyToml
	if _, err := toml.Decode(string(b), &ft); err != nil {
		return nil, fmt.Errorf("parse fly.toml: %w", err)
	}
	if ft.App == "" {
		return nil, fmt.Errorf("fly.toml missing app name")
	}
	res := &Result{Source: "fly"}
	name := sanitizeName(ft.App)

	// Volumes
	var vols []manifest.VolumeMount
	for _, m := range ft.Mounts {
		if m.Source == "" || m.Destination == "" {
			continue
		}
		vols = append(vols, manifest.VolumeMount{
			Name: sanitizeName(m.Source),
			Path: m.Destination,
		})
	}

	// Determine port from [http_service] or [[services]]
	port := ft.HTTPService.InternalPort
	if port == 0 && len(ft.Services) > 0 {
		port = ft.Services[0].InternalPort
	}

	// Image from [build] image (only used when Dockerfile is absent — we
	// don't second-guess "build dockerfile=...").
	image := ft.Build.Image

	// Env map
	env := map[string]string{}
	for k, v := range ft.Env {
		env[k] = v
	}

	if ft.PrimaryRegion != "" {
		res.Warnings = append(res.Warnings,
			fmt.Sprintf("primary_region=%q dropped — blob is single-region. Configure DNS/CDN externally if you need multi-region.", ft.PrimaryRegion))
	}
	if ft.Deploy.ReleaseCommand != "" {
		res.Warnings = append(res.Warnings,
			fmt.Sprintf("[deploy] release_command=%q dropped — blob does not auto-run release tasks. Run it manually with `blob exec` after deploy.",
				ft.Deploy.ReleaseCommand))
	}
	if len(ft.Checks) > 0 {
		res.Warnings = append(res.Warnings,
			"[[checks]] dropped — blob uses its own per-form healthchecks (TCP for web-service, no health for daemon).")
	}
	if len(ft.Statics) > 0 {
		res.Warnings = append(res.Warnings,
			"[[statics]] dropped — for static-only sites use form: static; otherwise serve assets from your app.")
	}
	if len(ft.VM) > 0 {
		res.Warnings = append(res.Warnings,
			"[[vm]] sizing dropped — set cpu/memory in blob.yaml directly if you need to override defaults.")
	}

	m := &manifest.Manifest{}
	if len(ft.Processes) > 1 {
		// Multi-process: each becomes a component. The first proc with a
		// web-shaped name keeps the http port; others become daemons.
		var procs []string
		for k := range ft.Processes {
			procs = append(procs, k)
		}
		// Stable order: app/web first
		stableProcs(procs)
		m.Name = name
		for _, p := range procs {
			c := manifest.Component{
				Name:    sanitizeName(p),
				Command: parseShellCmd(ft.Processes[p]),
				Env:     copyMap(env),
				Volumes: vols,
			}
			if isWebProc(p) && port > 0 {
				c.Form = "web-service"
				c.Port = port
				vols = nil // attach volume to the web proc only
			} else {
				c.Form = "daemon"
			}
			m.Components = append(m.Components, c)
		}
	} else {
		// Single process or none declared — use the implicit primary
		c := manifest.Component{Name: name, Env: env, Volumes: vols, Image: image}
		if port > 0 {
			c.Form = "web-service"
			c.Port = port
		} else {
			c.Form = "web-service"
			c.Port = 8080
			res.Warnings = append(res.Warnings, "no internal_port found — defaulted to 8080. Edit blob.yaml if your app listens elsewhere.")
		}
		// If a single named process was declared, use its command.
		if len(ft.Processes) == 1 {
			for _, cmd := range ft.Processes {
				c.Command = parseShellCmd(cmd)
			}
		}
		m.Component = c
	}
	res.Manifest = m
	if err := res.Render(); err != nil {
		return nil, err
	}
	return res, nil
}

// --- fly.toml schema ---

type flyToml struct {
	App           string                  `toml:"app"`
	PrimaryRegion string                  `toml:"primary_region"`
	Build         flyBuild                `toml:"build"`
	Env           map[string]string       `toml:"env"`
	Processes     map[string]string       `toml:"processes"`
	HTTPService   flyHTTPService          `toml:"http_service"`
	Services      []flyService            `toml:"services"`
	Mounts        []flyMount              `toml:"mounts"`
	Deploy        flyDeploy               `toml:"deploy"`
	Checks        map[string]any          `toml:"checks"`
	Statics       []map[string]any        `toml:"statics"`
	VM            []map[string]any        `toml:"vm"`
}

type flyBuild struct {
	Dockerfile string `toml:"dockerfile"`
	Image      string `toml:"image"`
}

type flyHTTPService struct {
	InternalPort int  `toml:"internal_port"`
	ForceHTTPS   bool `toml:"force_https"`
}

type flyService struct {
	InternalPort int `toml:"internal_port"`
}

type flyMount struct {
	Source      string `toml:"source"`
	Destination string `toml:"destination"`
}

type flyDeploy struct {
	ReleaseCommand string `toml:"release_command"`
}

func isWebProc(name string) bool {
	switch strings.ToLower(name) {
	case "app", "web", "http", "server":
		return true
	}
	return false
}

func stableProcs(procs []string) {
	// Web/app first, then alphabetical
	for i := range procs {
		if isWebProc(procs[i]) {
			procs[0], procs[i] = procs[i], procs[0]
			break
		}
	}
	rest := procs[1:]
	for i := 0; i < len(rest); i++ {
		for j := i + 1; j < len(rest); j++ {
			if rest[j] < rest[i] {
				rest[i], rest[j] = rest[j], rest[i]
			}
		}
	}
}

func copyMap(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
