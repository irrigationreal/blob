package importers

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/irrigationreal/blob/internal/manifest"
)

// Vercel parses vercel.json and emits a blob.yaml for Blob's static or
// Next.js deploy paths. Static projects map to form: static. Next.js projects
// with next.config present are delegated to the existing NextJS importer so the
// same standalone/static-output handling is used.
func Vercel(path string) (*Result, error) {
	path, err := vercelPath(path)
	if err != nil {
		return nil, err
	}
	dir := filepath.Dir(path)
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg vercelConfig
	if err := json.Unmarshal(b, &cfg); err != nil {
		return nil, fmt.Errorf("parse vercel.json: %w", err)
	}

	if isVercelNext(dir, cfg.Framework) {
		res, err := NextJS(dir)
		if err != nil {
			return nil, err
		}
		res.Source = "vercel"
		res.Warnings = append(res.Warnings, vercelWarnings(cfg)...)
		return res, nil
	}

	res := &Result{Source: "vercel"}
	name := sanitizeName(cfg.Name)
	if name == "" {
		name = sanitizeName(filepath.Base(dir))
	}
	root := strings.TrimSpace(cfg.OutputDirectory)
	explicitRoot := root != ""
	if root == "" {
		root = vercelBuildOutput(cfg)
	}
	if root == "" {
		root = vercelDetectedOutput(dir)
	}
	build := strings.TrimSpace(cfg.BuildCommand)
	if build == "" {
		build = vercelBuildCommand(dir, cfg.InstallCommand)
	}
	env := map[string]string{}
	for k, v := range cfg.Env {
		env[k] = fmt.Sprint(v)
	}
	for k, v := range cfg.Build.Env {
		if _, ok := env[k]; !ok {
			env[k] = fmt.Sprint(v)
		}
	}
	c := manifest.Component{
		Name:  name,
		Form:  "static",
		Root:  root,
		Build: build,
		Env:   env,
	}
	if len(c.Env) == 0 {
		c.Env = nil
	}
	res.Manifest = &manifest.Manifest{Component: c}
	res.Warnings = append(res.Warnings, vercelWarnings(cfg)...)
	if cfg.Framework != "" && cfg.Framework != "static" {
		res.Warnings = append(res.Warnings, fmt.Sprintf("framework=%q imported as static output - use blob import nextjs for server-rendered Next.js apps", cfg.Framework))
	}
	if !explicitRoot && root == "." {
		res.Warnings = append(res.Warnings, "no outputDirectory found - defaulted static root to .; edit root: if your build writes dist/build/out/public")
	}
	if err := res.Render(); err != nil {
		return nil, err
	}
	return res, nil
}

type vercelConfig struct {
	Name            string         `json:"name"`
	Framework       string         `json:"framework"`
	BuildCommand    string         `json:"buildCommand"`
	InstallCommand  string         `json:"installCommand"`
	OutputDirectory string         `json:"outputDirectory"`
	Env             map[string]any `json:"env"`
	Build           struct {
		Env map[string]any `json:"env"`
	} `json:"build"`
	Builds        []vercelBuild    `json:"builds"`
	Routes        []map[string]any `json:"routes"`
	Rewrites      []map[string]any `json:"rewrites"`
	Redirects     []map[string]any `json:"redirects"`
	Headers       []map[string]any `json:"headers"`
	Functions     map[string]any   `json:"functions"`
	Crons         []map[string]any `json:"crons"`
	CleanUrls     any              `json:"cleanUrls"`
	TrailingSlash any              `json:"trailingSlash"`
}

type vercelBuild struct {
	Src    string         `json:"src"`
	Use    string         `json:"use"`
	Config map[string]any `json:"config"`
}

func vercelPath(path string) (string, error) {
	if fi, err := os.Stat(path); err == nil && fi.IsDir() {
		p := filepath.Join(path, "vercel.json")
		if _, err := os.Stat(p); err != nil {
			return "", fmt.Errorf("no vercel.json in %s", path)
		}
		return p, nil
	}
	return path, nil
}

func isVercelNext(dir, framework string) bool {
	if strings.EqualFold(framework, "nextjs") && (exists(dir, "next.config.js") || exists(dir, "next.config.mjs") || exists(dir, "next.config.ts")) {
		return true
	}
	if !exists(dir, "package.json") || !(exists(dir, "next.config.js") || exists(dir, "next.config.mjs") || exists(dir, "next.config.ts")) {
		return false
	}
	b, err := os.ReadFile(filepath.Join(dir, "package.json"))
	return err == nil && strings.Contains(string(b), `"next"`)
}

func vercelDetectedOutput(dir string) string {
	for _, name := range []string{"dist", "build", "out", "public"} {
		if fi, err := os.Stat(filepath.Join(dir, name)); err == nil && fi.IsDir() {
			return name
		}
	}
	return "."
}

func vercelBuildOutput(cfg vercelConfig) string {
	for _, b := range cfg.Builds {
		if !strings.Contains(b.Use, "static-build") {
			continue
		}
		for _, key := range []string{"distDir", "outputDirectory"} {
			if v, ok := b.Config[key]; ok && strings.TrimSpace(fmt.Sprint(v)) != "" {
				return strings.TrimSpace(fmt.Sprint(v))
			}
		}
	}
	return ""
}

func vercelBuildCommand(dir, installCommand string) string {
	if !exists(dir, "package.json") {
		return ""
	}
	buildCommand := "npm run build"
	installCommand = strings.TrimSpace(installCommand)
	switch {
	case exists(dir, "pnpm-lock.yaml"):
		buildCommand = "pnpm run build"
		if installCommand == "" {
			installCommand = "corepack enable pnpm && pnpm install --no-frozen-lockfile"
		}
	case exists(dir, "yarn.lock"):
		buildCommand = "yarn build"
		if installCommand == "" {
			installCommand = "yarn install"
		}
	case exists(dir, "bun.lock"), exists(dir, "bun.lockb"):
		buildCommand = "bun run build"
		if installCommand == "" {
			installCommand = "npm i -g bun && bun install"
		}
	default:
		if installCommand == "" {
			installCommand = "npm install"
		}
	}
	return installCommand + " && " + buildCommand
}

func vercelWarnings(cfg vercelConfig) []string {
	var warnings []string
	if len(cfg.Routes) > 0 {
		warnings = append(warnings, fmt.Sprintf("%d legacy routes dropped - translate critical routing to app code or Traefik/Caddy config", len(cfg.Routes)))
	}
	if len(cfg.Rewrites) > 0 {
		warnings = append(warnings, fmt.Sprintf("%d rewrites dropped - Blob routes the whole host to the app", len(cfg.Rewrites)))
	}
	if len(cfg.Redirects) > 0 {
		warnings = append(warnings, fmt.Sprintf("%d redirects dropped - implement them in the app or edge config", len(cfg.Redirects)))
	}
	if len(cfg.Headers) > 0 {
		warnings = append(warnings, fmt.Sprintf("%d headers dropped - set them in the app or edge config", len(cfg.Headers)))
	}
	if len(cfg.Functions) > 0 {
		warnings = append(warnings, "Vercel functions were not auto-translated; move backend code into form:function or a web-service")
	}
	if len(cfg.Crons) > 0 {
		warnings = append(warnings, "Vercel crons dropped - recreate them with `blob jobs schedule`")
	}
	for _, b := range cfg.Builds {
		if strings.Contains(b.Use, "static-build") {
			continue
		}
		if b.Use != "" {
			warnings = append(warnings, fmt.Sprintf("build %q uses %q - only static-build maps directly; verify generated blob.yaml", b.Src, b.Use))
		}
	}
	return warnings
}
