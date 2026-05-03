package importers

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"

	"github.com/darvell/blob/internal/manifest"
)

// Netlify parses a netlify.toml at path and returns a blob.yaml shaped
// for the existing static-site deploy path. `path` may be either:
//   - the explicit netlify.toml file, or
//   - a directory containing a netlify.toml.
//
//   - [build] command  → blob.yaml `build:`
//   - [build] publish  → blob.yaml `root:` (the directory served by Caddy)
//   - [build.environment] → env: map
//
// Things we explicitly drop with a warning:
//   - [[redirects]] / [[headers]] — Caddy config differs; emit a TODO so
//     the operator can hand-translate
//   - [functions] — Netlify edge functions are out of scope this ship
//     (deferred to v0.13)
//   - [[plugins]] — build plugins; not portable to the blobd build path
func Netlify(path string) (*Result, error) {
	// Accept a directory: look for netlify.toml inside.
	if fi, err := os.Stat(path); err == nil && fi.IsDir() {
		path = filepath.Join(path, "netlify.toml")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var nt netlifyToml
	if _, err := toml.Decode(string(b), &nt); err != nil {
		return nil, fmt.Errorf("parse netlify.toml: %w", err)
	}
	res := &Result{Source: "netlify"}

	dir := filepath.Dir(path)
	// Resolve to absolute so `.` becomes a real basename.
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}
	name := sanitizeName(filepath.Base(dir))
	if name == "" {
		name = "site"
	}

	c := manifest.Component{
		Name: name,
		Form: "static",
	}
	if nt.Build.Publish != "" {
		c.Root = nt.Build.Publish
	} else {
		c.Root = "."
	}
	if nt.Build.Command != "" {
		c.Build = nt.Build.Command
	}
	if env := nt.Build.Environment; len(env) > 0 {
		c.Env = env
	}

	if len(nt.Redirects) > 0 {
		res.Warnings = append(res.Warnings,
			fmt.Sprintf("%d [[redirects]] entries dropped — Caddy/Traefik handle redirects differently. Translate critical redirects to a Caddyfile or app-level routes.",
				len(nt.Redirects)))
	}
	if len(nt.Headers) > 0 {
		res.Warnings = append(res.Warnings,
			fmt.Sprintf("%d [[headers]] entries dropped — set custom headers via your origin app or a Traefik middleware.",
				len(nt.Headers)))
	}
	if nt.Functions.Directory != "" || len(nt.EdgeFunctions) > 0 {
		res.Warnings = append(res.Warnings,
			"netlify functions / edge functions are not yet supported (planned for v0.13). The static publish dir was imported; backend code was not.")
	}
	if len(nt.Plugins) > 0 {
		res.Warnings = append(res.Warnings,
			fmt.Sprintf("%d [[plugins]] entries dropped — build plugins aren't portable to the blobd build path; replicate their effects in the build command if you need them.",
				len(nt.Plugins)))
	}

	m := &manifest.Manifest{Component: c}
	res.Manifest = m
	if err := res.Render(); err != nil {
		return nil, err
	}
	return res, nil
}

type netlifyToml struct {
	Build struct {
		Command     string            `toml:"command"`
		Publish     string            `toml:"publish"`
		Environment map[string]string `toml:"environment"`
	} `toml:"build"`
	Redirects     []map[string]any `toml:"redirects"`
	Headers       []map[string]any `toml:"headers"`
	Functions     struct {
		Directory string `toml:"directory"`
	} `toml:"functions"`
	EdgeFunctions []map[string]any `toml:"edge_functions"`
	Plugins       []map[string]any `toml:"plugins"`
}
