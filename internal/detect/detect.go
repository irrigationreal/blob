// Package detect infers a sensible blob.yaml from a project folder.
//
// Order of detection:
//
//  1. blob.yaml present → return that (handled by caller)
//  2. Dockerfile present → web-service
//  3. compose file present → web-service (compose path)
//  4. index.html at root → static (root=".")
//  5. package.json with a build script → static, build="<pm> install && <pm> run build", auto-pick dist/build dir
//  6. otherwise → leave empty so the user is forced to be explicit
package detect

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/irrigationreal/blob/internal/manifest"
)

// Detect returns a Component populated with reasonable defaults for the given
// folder, plus a short human description of what was inferred.
func Detect(dir string) (*manifest.Component, string) {
	c := &manifest.Component{Name: defaultName(dir)}
	if exists(dir, "Dockerfile") {
		c.Form = "web-service"
		return c, "Dockerfile detected"
	}
	if hasComposeFile(dir) {
		c.Form = "web-service"
		return c, "Compose file detected"
	}
	if exists(dir, "index.html") {
		c.Form = "static"
		c.Root = "."
		return c, "static site (index.html at root)"
	}
	if exists(dir, "package.json") {
		c.Form = "static"
		if build := pickBuildCommand(dir); build != "" {
			c.Build = build
		}
		return c, "package.json detected, treating as static SPA build"
	}
	if exists(dir, "Procfile") {
		c.Form = "web-service"
		return c, "Procfile detected (use a buildpack-builder Dockerfile or convert manually)"
	}
	if exists(dir, "fly.toml") {
		c.Form = "web-service"
		return c, "fly.toml detected; copy any custom port/internal_port to blob.yaml"
	}
	c.Form = "web-service"
	return c, "no obvious project type — defaulting to web-service; add a Dockerfile or set form: static"
}

func defaultName(dir string) string {
	abs, err := filepath.Abs(dir)
	if err != nil {
		abs = dir
	}
	name := strings.ToLower(filepath.Base(abs))
	// sanitise into a valid blob name
	var b strings.Builder
	prevDash := false
	for _, r := range name {
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
	out := strings.Trim(b.String(), "-")
	if out == "" {
		out = "app"
	}
	return out
}

func exists(dir, name string) bool {
	_, err := os.Stat(filepath.Join(dir, name))
	return err == nil
}

func hasComposeFile(dir string) bool {
	for _, n := range []string{"compose.yaml", "compose.yml", "docker-compose.yaml", "docker-compose.yml"} {
		if exists(dir, n) {
			return true
		}
	}
	return false
}

// pickBuildCommand reads package.json scripts and chooses a "build" script.
// Picks the package manager based on which lockfile is present.
func pickBuildCommand(dir string) string {
	b, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		return ""
	}
	var pkg struct {
		Scripts map[string]string `json:"scripts"`
	}
	if err := json.Unmarshal(b, &pkg); err != nil {
		return ""
	}
	if _, ok := pkg.Scripts["build"]; !ok {
		return ""
	}
	pm := "npm"
	if exists(dir, "pnpm-lock.yaml") {
		pm = "pnpm"
	} else if exists(dir, "yarn.lock") {
		pm = "yarn"
	} else if exists(dir, "bun.lockb") {
		pm = "bun"
	}
	switch pm {
	case "pnpm":
		return "pnpm install && pnpm run build"
	case "yarn":
		return "yarn install && yarn build"
	case "bun":
		return "bun install && bun run build"
	default:
		return "npm ci && npm run build"
	}
}
