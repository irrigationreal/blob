package importers

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/irrigationreal/blob/internal/manifest"
)

// NextJS detects a Next.js project at dir and emits:
//
//   1. a multi-stage Dockerfile (deps → builder → runner) using the
//      `output: 'standalone'` build mode for ~50 MB runtime images, and
//   2. a blob.yaml pointing at port 3000.
//
// Both files land in dir. We refuse to overwrite either unless --yes is
// passed by the CLI (the cmdImport wrapper handles that).
//
// We honor `basePath` from next.config.{js,ts,mjs} when set — projects
// hosted under a sub-path receive Domains for the bare host PLUS a path
// hint in the warnings. We do NOT try to fully parse next.config; a
// regex over the file is good enough for the 80% case (basePath is
// typically a simple string literal).
func NextJS(dir string) (*Result, error) {
	if !exists(dir, "package.json") {
		return nil, fmt.Errorf("no package.json in %s", dir)
	}
	pkgRaw, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		return nil, err
	}
	var pkg struct {
		Name         string            `json:"name"`
		Dependencies map[string]string `json:"dependencies"`
		Scripts      map[string]string `json:"scripts"`
	}
	if err := json.Unmarshal(pkgRaw, &pkg); err != nil {
		return nil, fmt.Errorf("parse package.json: %w", err)
	}
	if _, ok := pkg.Dependencies["next"]; !ok {
		return nil, fmt.Errorf("package.json has no \"next\" dependency — not a Next.js project")
	}
	cfgPath := ""
	for _, n := range []string{"next.config.ts", "next.config.js", "next.config.mjs"} {
		if exists(dir, n) {
			cfgPath = filepath.Join(dir, n)
			break
		}
	}
	if cfgPath == "" {
		return nil, fmt.Errorf("no next.config.{ts,js,mjs} in %s", dir)
	}

	res := &Result{Source: "nextjs"}
	name := sanitizeName(pkg.Name)
	if name == "" {
		name = sanitizeName(filepath.Base(dir))
	}

	// Try to extract basePath. Best-effort regex; ignored if not present.
	basePath := ""
	if b, err := os.ReadFile(cfgPath); err == nil {
		if m := regexp.MustCompile(`basePath\s*:\s*["']([^"']+)["']`).FindStringSubmatch(string(b)); len(m) == 2 {
			basePath = m[1]
		}
	}
	if basePath != "" {
		res.Warnings = append(res.Warnings,
			fmt.Sprintf("next.config has basePath=%q — Traefik routes the entire host to this app, so the basePath only affects the app's internal routes; no extra config needed",
				basePath))
	}

	// Detect package manager. The Dockerfile install step picks accordingly.
	pm := "npm"
	switch {
	case exists(dir, "pnpm-lock.yaml"):
		pm = "pnpm"
	case exists(dir, "yarn.lock"):
		pm = "yarn"
	case exists(dir, "bun.lockb"), exists(dir, "bun.lock"):
		pm = "bun"
	}

	// Inspect (and possibly mutate) next.config to align with one of two
	// supported deploy paths:
	//
	//   output: 'export'      → static-site form (Caddy serves ./out)
	//   output: 'standalone'  → web-service form, the standard 3-stage Dockerfile
	//   output: <unset|other> → inject `output: 'standalone'` into next.config
	cfgRaw, err := os.ReadFile(cfgPath)
	if err != nil {
		return nil, err
	}
	mode, modified, newCfg := detectAndAlignNextOutput(string(cfgRaw))

	res.ExtraFiles = map[string][]byte{}
	if modified {
		res.ExtraFiles[filepath.Base(cfgPath)] = []byte(newCfg)
		res.Warnings = append(res.Warnings,
			fmt.Sprintf("modified %s to add output: 'standalone' (.next/standalone is required by the generated Dockerfile)",
				filepath.Base(cfgPath)))
	}

	switch mode {
	case "export":
		// Static-site path: `next build` writes ./out, Caddy serves it.
		c := manifest.Component{
			Name:  name,
			Form:  "static",
			Root:  "out",
			Build: nextBuildCmd(pm),
		}
		res.Manifest = &manifest.Manifest{Component: c}
		if err := res.Render(); err != nil {
			return nil, err
		}
		res.Warnings = append(res.Warnings,
			"next.config has output: 'export' — emitting form: static (Caddy serves ./out). Server-rendered routes / API routes / dynamic params won't work in this mode.")
		return res, nil
	default:
		// "standalone" path: 3-stage Docker build runs the standalone server.
		df := buildNextDockerfile(pm)
		if exists(dir, "Dockerfile") {
			res.Warnings = append(res.Warnings,
				"a Dockerfile already exists at this path; the importer left it alone. Pass --yes to overwrite via the CLI's existing-file check.")
		}
		c := manifest.Component{
			Name: name,
			Form: "web-service",
			Port: 3000,
		}
		res.Manifest = &manifest.Manifest{Component: c}
		if err := res.Render(); err != nil {
			return nil, err
		}
		res.ExtraFiles["Dockerfile"] = []byte(df)
		res.ExtraFiles[".dockerignore"] = []byte(nextDockerignore)
		return res, nil
	}
}

// detectAndAlignNextOutput resolves the output mode of a next.config
// source and, when the project has no `output:` key at all (or has an
// unsupported value), rewrites the source to inject
// `output: 'standalone'` so the standard Docker pipeline works.
//
//	mode == "export"     → existing `output: 'export'` (file unchanged)
//	mode == "standalone" → existing `output: 'standalone'` (file unchanged)
//	mode == "standalone" with modified=true → we injected the key
//
// Injection target: the first `{` after one of the known config-object
// markers (`NextConfig =`, `export default {`, `module.exports =`).
// `\n  output: 'standalone',` is added immediately inside that brace.
// If we can't find a brace, modified is false and the caller surfaces a
// warning.
func detectAndAlignNextOutput(src string) (mode string, modified bool, out string) {
	re := regexp.MustCompile(`output\s*:\s*["']([^"']+)["']`)
	if m := re.FindStringSubmatch(src); len(m) == 2 {
		switch m[1] {
		case "export":
			return "export", false, src
		case "standalone":
			return "standalone", false, src
		default:
			return "standalone", true, re.ReplaceAllString(src, `output: 'standalone'`)
		}
	}
	idx := -1
	for _, marker := range []string{
		"NextConfig =",
		"NextConfig=",
		"export default {",
		"module.exports =",
		"module.exports={",
	} {
		if i := strings.Index(src, marker); i >= 0 {
			if b := strings.Index(src[i:], "{"); b >= 0 {
				idx = i + b
				break
			}
		}
	}
	// Fallback: a bare `const <name> = {` pattern. We look for the first
	// `= {` that's preceded by an identifier-style token. This catches
	// .mjs files like:
	//
	//   /** @type {import('next').NextConfig} */
	//   const nextConfig = { ... }
	//
	// where the explicit markers above don't fire.
	if idx == -1 {
		bareRe := regexp.MustCompile(`(?m)^\s*(?:const|let|var)\s+\w+\s*=\s*\{`)
		if loc := bareRe.FindStringIndex(src); loc != nil {
			// position the injection right after the opening brace
			if b := strings.Index(src[loc[0]:loc[1]], "{"); b >= 0 {
				idx = loc[0] + b
			}
		}
	}
	if idx == -1 {
		return "standalone", false, src
	}
	out = src[:idx+1] + "\n  output: 'standalone'," + src[idx+1:]
	return "standalone", true, out
}

// nextBuildCmd returns the build command for the given package manager.
// Used in the form: static path where blobd's static-build pipeline runs
// it from the source dir. Lockfile drift is tolerated (no --frozen-lockfile
// / npm ci) so an unchanged checkout from years-ago still builds.
func nextBuildCmd(pm string) string {
	switch pm {
	case "pnpm":
		return "corepack enable pnpm && pnpm install --no-frozen-lockfile && pnpm run build"
	case "yarn":
		return "yarn install && yarn build"
	case "bun":
		return "bun install && bun run build"
	default:
		return "npm install && npm run build"
	}
}

// buildNextDockerfile emits a Dockerfile tuned for `next build` with
// `output: 'standalone'`. The runtime stage is intentionally minimal —
// node:alpine runs the standalone server.js with /app/.next/static and
// /app/public mounted in.
//
// The install step deliberately does NOT use --frozen-lockfile / npm ci.
// An importer parachuting into someone else's project shouldn't fail
// the build because the lockfile has drifted; if the operator wants the
// strict guarantee they can edit the Dockerfile.
func buildNextDockerfile(pm string) string {
	var install, build string
	switch pm {
	case "pnpm":
		install = `RUN corepack enable pnpm && pnpm install --no-frozen-lockfile`
		build = `RUN corepack enable pnpm && pnpm run build`
	case "yarn":
		install = `RUN yarn install`
		build = `RUN yarn build`
	case "bun":
		install = `RUN apk add --no-cache bash && \
    npm i -g bun && bun install`
		build = `RUN bun run build`
	default:
		install = `RUN npm install`
		build = `RUN npm run build`
	}
	return fmt.Sprintf(`# syntax=docker/dockerfile:1.7
# Generated by 'blob from-nextjs' — Next.js standalone build.
#
# stage 1: install deps
FROM node:20-alpine AS deps
WORKDIR /app
COPY package.json *.lock* yarn.lock* package-lock.json* pnpm-lock.yaml* bun.lockb* bun.lock* ./
%s

# stage 2: build the standalone bundle
FROM node:20-alpine AS builder
WORKDIR /app
COPY --from=deps /app/node_modules ./node_modules
COPY . .
ENV NEXT_TELEMETRY_DISABLED=1
%s

# stage 3: runtime — only the .next/standalone server + static + public
FROM node:20-alpine AS runner
WORKDIR /app
ENV NODE_ENV=production
ENV NEXT_TELEMETRY_DISABLED=1
ENV PORT=3000
ENV HOSTNAME=0.0.0.0
RUN addgroup -g 1001 -S nodejs && adduser -u 1001 -S nextjs -G nodejs
COPY --from=builder /app/public ./public
COPY --from=builder --chown=nextjs:nodejs /app/.next/standalone ./
COPY --from=builder --chown=nextjs:nodejs /app/.next/static ./.next/static
USER nextjs
EXPOSE 3000
CMD ["node", "server.js"]
`, install, build)
}

const nextDockerignore = `node_modules
.next
.git
.gitignore
*.md
DEPLOY.md
dogfood-output
.env*
!.env.production
npm-debug.log*
yarn-debug.log*
yarn-error.log*
.DS_Store
`
