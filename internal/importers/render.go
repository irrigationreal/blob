package importers

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/darvell/blob/internal/manifest"
)

// Render parses a render.yaml Blueprint and returns a blob.yaml.
//
// Supported 80% mapping:
//   - static sites: staticPublishPath/runtime: static -> form: static
//   - web services: type: web -> web-service, defaulting PORT to 10000
//   - workers/private services: type: worker/pserv -> daemon
//   - cron services: type: cron -> cronjob
//   - envVars with literal value -> env
//   - disk mount -> volumes
//   - node/python/go services without Dockerfile get a generated Dockerfile
//
// Render-managed resources, env groups, regions, headers/routes, databases,
// and secrets are warned so the operator can recreate them with Blob services
// and secrets.
func Render(path string) (*Result, error) {
	path, err := renderPath(path)
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var bp renderBlueprint
	if err := yaml.Unmarshal(b, &bp); err != nil {
		return nil, fmt.Errorf("parse render.yaml: %w", err)
	}
	if len(bp.Services) == 0 {
		return nil, fmt.Errorf("render.yaml has no services")
	}

	res := &Result{Source: "render", ExtraFiles: map[string][]byte{}}
	dir := filepath.Dir(path)
	projectName := sanitizeName(filepath.Base(dir))

	if len(bp.Databases) > 0 {
		res.Warnings = append(res.Warnings, fmt.Sprintf("%d Render databases dropped - create Blob managed services and bind them via services: [...]", len(bp.Databases)))
	}
	if len(bp.EnvVarGroups) > 0 {
		res.Warnings = append(res.Warnings, fmt.Sprintf("%d envVarGroups dropped - create Blob secrets and bind them in blob.yaml", len(bp.EnvVarGroups)))
	}

	components := make([]manifest.Component, 0, len(bp.Services))
	for _, svc := range bp.Services {
		name := sanitizeName(svc.Name)
		if name == "" {
			name = projectName
		}
		kind := strings.ToLower(strings.TrimSpace(svc.Type))
		runtime := strings.ToLower(strings.TrimSpace(firstNonEmpty(svc.Runtime, svc.Env)))
		if kind == "" {
			kind = "web"
		}
		if renderStatefulService(kind, runtime) {
			res.Warnings = append(res.Warnings, fmt.Sprintf("service %q is Render type/runtime %q/%q and looks stateful - skipped; create a Blob managed service instead", svc.Name, svc.Type, firstNonEmpty(svc.Runtime, svc.Env)))
			continue
		}

		env, warnings := renderEnvVars(svc.EnvVars, name)
		res.Warnings = append(res.Warnings, warnings...)
		port := renderPort(env)
		if port == 0 {
			port = 10000
		}

		c := manifest.Component{Name: name, Env: env}
		if svc.NumInstances > 0 {
			c.Replicas = svc.NumInstances
		}
		if svc.Disk.Name != "" && svc.Disk.MountPath != "" {
			c.Volumes = append(c.Volumes, manifest.VolumeMount{Name: sanitizeName(svc.Disk.Name), Path: svc.Disk.MountPath})
		}
		for _, d := range svc.Disks {
			if d.Name != "" && d.MountPath != "" {
				c.Volumes = append(c.Volumes, manifest.VolumeMount{Name: sanitizeName(d.Name), Path: d.MountPath})
			}
		}

		switch {
		case renderStaticService(kind, runtime, svc.StaticPublishPath):
			c.Form = "static"
			c.Root = renderRoot(svc.RootDir, firstNonEmpty(svc.StaticPublishPath, "."))
			c.Build = renderBuildCommand(svc.RootDir, svc.BuildCommand)
			if len(svc.Headers) > 0 || len(svc.Routes) > 0 {
				res.Warnings = append(res.Warnings, fmt.Sprintf("static service %q has Render headers/routes - translate critical rules to the app or edge config manually", svc.Name))
			}
		case kind == "cron":
			c.Form = "cronjob"
			c.Schedule = firstNonEmpty(svc.Schedule, svc.CronSchedule)
			c.Command = parseShellCmd(svc.StartCommand)
			if c.Schedule == "" {
				res.Warnings = append(res.Warnings, fmt.Sprintf("cron service %q has no schedule - add schedule: before deploy", svc.Name))
			}
		case kind == "worker" || kind == "pserv" || kind == "private_service":
			c.Form = "daemon"
			c.Command = parseShellCmd(svc.StartCommand)
		default:
			c.Form = "web-service"
			c.Port = port
			if c.Env == nil {
				c.Env = map[string]string{}
			}
			if _, ok := c.Env["PORT"]; !ok {
				c.Env["PORT"] = fmt.Sprint(port)
			}
			c.Command = parseShellCmd(svc.StartCommand)
		}

		if image := renderImage(svc.Image); image != "" {
			c.Image = image
		}
		if svc.Region != "" {
			res.Warnings = append(res.Warnings, fmt.Sprintf("service %q region=%q dropped - Blob is single-region", svc.Name, svc.Region))
		}
		if svc.HealthCheckPath != "" {
			res.Warnings = append(res.Warnings, fmt.Sprintf("service %q healthCheckPath=%q dropped - Blob uses its own service checks", svc.Name, svc.HealthCheckPath))
		}
		if svc.Plan != "" {
			res.Warnings = append(res.Warnings, fmt.Sprintf("service %q plan=%q dropped - set cpu/memory in blob.yaml if needed", svc.Name, svc.Plan))
		}
		if svc.DockerfilePath != "" && svc.DockerfilePath != "Dockerfile" {
			res.Warnings = append(res.Warnings, fmt.Sprintf("service %q dockerfilePath=%q - Blob builds Dockerfile at the app root; move or rename it if needed", svc.Name, svc.DockerfilePath))
		}
		if svc.DockerContext != "" && svc.DockerContext != "." {
			res.Warnings = append(res.Warnings, fmt.Sprintf("service %q dockerContext=%q dropped - run import/deploy from that directory or adjust Dockerfile paths", svc.Name, svc.DockerContext))
		}

		if c.Image == "" && c.Form != "static" && !exists(dir, "Dockerfile") {
			if df := renderServiceDockerfile(runtime, svc.RootDir, svc.BuildCommand, svc.StartCommand, port); df != "" {
				res.ExtraFiles["Dockerfile"] = []byte(df)
				res.ExtraFiles[".dockerignore"] = []byte(genericDockerignore)
			} else {
				res.Warnings = append(res.Warnings, fmt.Sprintf("service %q has runtime=%q but no Dockerfile; add one before deploy", svc.Name, firstNonEmpty(svc.Runtime, svc.Env)))
			}
		}

		components = append(components, c)
	}
	if len(components) == 0 {
		return nil, fmt.Errorf("render.yaml has no translatable services")
	}
	m := &manifest.Manifest{}
	if len(components) == 1 {
		m.Component = components[0]
		if m.Name == "" {
			m.Name = components[0].Name
		}
	} else {
		m.Name = projectName
		m.Components = components
	}
	res.Manifest = m
	if len(res.ExtraFiles) == 0 {
		res.ExtraFiles = nil
	}
	if err := res.Render(); err != nil {
		return nil, err
	}
	return res, nil
}

type renderBlueprint struct {
	Services     []renderService  `yaml:"services"`
	Databases    []map[string]any `yaml:"databases"`
	EnvVarGroups []map[string]any `yaml:"envVarGroups"`
}

type renderService struct {
	Type              string           `yaml:"type"`
	Name              string           `yaml:"name"`
	Runtime           string           `yaml:"runtime"`
	Env               string           `yaml:"env"`
	Plan              string           `yaml:"plan"`
	Region            string           `yaml:"region"`
	BuildCommand      string           `yaml:"buildCommand"`
	StartCommand      string           `yaml:"startCommand"`
	StaticPublishPath string           `yaml:"staticPublishPath"`
	RootDir           string           `yaml:"rootDir"`
	Schedule          string           `yaml:"schedule"`
	CronSchedule      string           `yaml:"cronSchedule"`
	DockerfilePath    string           `yaml:"dockerfilePath"`
	DockerContext     string           `yaml:"dockerContext"`
	HealthCheckPath   string           `yaml:"healthCheckPath"`
	NumInstances      int              `yaml:"numInstances"`
	EnvVars           []renderEnvVar   `yaml:"envVars"`
	Disk              renderDisk       `yaml:"disk"`
	Disks             []renderDisk     `yaml:"disks"`
	Headers           []map[string]any `yaml:"headers"`
	Routes            []map[string]any `yaml:"routes"`
	Image             any              `yaml:"image"`
}

type renderEnvVar struct {
	Key           string `yaml:"key"`
	Value         any    `yaml:"value"`
	FromGroup     string `yaml:"fromGroup"`
	GenerateValue bool   `yaml:"generateValue"`
	Sync          any    `yaml:"sync"`
	FromDatabase  any    `yaml:"fromDatabase"`
	FromService   any    `yaml:"fromService"`
}

type renderDisk struct {
	Name      string `yaml:"name"`
	MountPath string `yaml:"mountPath"`
}

func renderPath(path string) (string, error) {
	if fi, err := os.Stat(path); err == nil && fi.IsDir() {
		for _, name := range []string{"render.yaml", "render.yml"} {
			p := filepath.Join(path, name)
			if _, err := os.Stat(p); err == nil {
				return p, nil
			}
		}
		return "", fmt.Errorf("no render.yaml or render.yml in %s", path)
	}
	return path, nil
}

func renderEnvVars(vars []renderEnvVar, service string) (map[string]string, []string) {
	out := map[string]string{}
	var warnings []string
	for _, ev := range vars {
		key := strings.TrimSpace(ev.Key)
		if key == "" {
			if ev.FromGroup != "" {
				warnings = append(warnings, fmt.Sprintf("service %q references env group %q - create Blob secrets manually", service, ev.FromGroup))
			}
			continue
		}
		if ev.Value != nil {
			out[key] = fmt.Sprint(ev.Value)
			continue
		}
		warnings = append(warnings, fmt.Sprintf("service %q env var %s is Render-managed or secret-backed - create a Blob secret and bind it manually", service, key))
	}
	return out, warnings
}

func renderPort(env map[string]string) int {
	for _, key := range []string{"PORT", "port"} {
		if v := env[key]; v != "" {
			return parsePort(v)
		}
	}
	return 0
}

func renderStaticService(kind, runtime, publish string) bool {
	return kind == "static" || kind == "static_site" || runtime == "static" || publish != ""
}

func renderStatefulService(kind, runtime string) bool {
	s := kind + ":" + runtime
	for _, marker := range []string{"keyvalue", "redis", "postgres", "database"} {
		if strings.Contains(s, marker) {
			return true
		}
	}
	return false
}

func renderRoot(rootDir, publish string) string {
	publish = strings.TrimPrefix(strings.TrimSpace(publish), "./")
	if publish == "" {
		publish = "."
	}
	rootDir = strings.TrimPrefix(strings.TrimSpace(rootDir), "./")
	if rootDir == "" || rootDir == "." {
		return publish
	}
	if publish == "." {
		return filepath.ToSlash(rootDir)
	}
	return filepath.ToSlash(filepath.Join(rootDir, publish))
}

func renderBuildCommand(rootDir, cmd string) string {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return ""
	}
	rootDir = strings.TrimPrefix(strings.TrimSpace(rootDir), "./")
	if rootDir == "" || rootDir == "." {
		return cmd
	}
	return "cd " + shellQuote(rootDir) + " && " + cmd
}

func renderImage(image any) string {
	switch v := image.(type) {
	case string:
		return strings.TrimSpace(v)
	case map[string]any:
		if url, ok := v["url"]; ok {
			return strings.TrimSpace(fmt.Sprint(url))
		}
	}
	return ""
}

func renderServiceDockerfile(runtime, rootDir, buildCommand, startCommand string, port int) string {
	runtime = strings.ToLower(strings.TrimSpace(runtime))
	workdir := "/app"
	rootDir = strings.Trim(strings.TrimSpace(rootDir), "/")
	rootDir = strings.TrimPrefix(rootDir, "./")
	if rootDir != "" && rootDir != "." {
		workdir = filepath.ToSlash(filepath.Join("/app", rootDir))
	}
	startCommand = strings.TrimSpace(startCommand)
	if startCommand == "" {
		return ""
	}
	build := ""
	if strings.TrimSpace(buildCommand) != "" {
		build = "RUN " + buildCommand + "\n"
	}
	switch runtime {
	case "node", "nodejs":
		return fmt.Sprintf(`FROM node:20-alpine
WORKDIR /app
COPY package.json package-lock.json* pnpm-lock.yaml* yarn.lock* bun.lock* bun.lockb* ./
RUN if [ -f pnpm-lock.yaml ]; then corepack enable pnpm && pnpm install --no-frozen-lockfile; elif [ -f yarn.lock ]; then yarn install; elif [ -f bun.lock ] || [ -f bun.lockb ]; then npm i -g bun && bun install; elif [ -f package.json ]; then npm install; fi
COPY . .
WORKDIR %s
%sENV NODE_ENV=production
ENV PORT=%d
EXPOSE %d
CMD ["sh", "-lc", %q]
`, workdir, build, port, port, startCommand)
	case "python", "python3":
		return fmt.Sprintf(`FROM python:3.12-slim
WORKDIR /app
COPY . .
WORKDIR %s
RUN if [ -f requirements.txt ]; then pip install --no-cache-dir -r requirements.txt; fi
%sENV PORT=%d
EXPOSE %d
CMD ["sh", "-lc", %q]
`, workdir, build, port, port, startCommand)
	case "go", "golang":
		cmd := "go build -o /out/app ."
		if strings.TrimSpace(buildCommand) != "" {
			cmd = buildCommand
		}
		return fmt.Sprintf(`FROM golang:1.25-alpine AS builder
WORKDIR /src
COPY . .
WORKDIR %s
RUN mkdir -p /out && %s
FROM alpine:3.21
WORKDIR /app
COPY --from=builder /out/app /app/app
ENV PORT=%d
EXPOSE %d
CMD ["/app/app"]
`, strings.Replace(workdir, "/app", "/src", 1), cmd, port, port)
	}
	return ""
}

func firstNonEmpty(xs ...string) string {
	for _, x := range xs {
		if strings.TrimSpace(x) != "" {
			return strings.TrimSpace(x)
		}
	}
	return ""
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

const genericDockerignore = `.git
node_modules
vendor
result
.env*
.DS_Store
`
