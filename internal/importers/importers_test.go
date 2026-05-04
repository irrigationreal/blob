package importers

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestRenderStaticImporter(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "render.yaml"), `services:
  - type: web
    name: Docs Site
    runtime: static
    rootDir: web
    buildCommand: npm run build
    staticPublishPath: dist
    envVars:
      - key: API_BASE
        value: https://api.example.com
`)
	res, err := Render(dir)
	if err != nil {
		t.Fatal(err)
	}
	if res.Source != "render" {
		t.Fatalf("source = %q", res.Source)
	}
	m := res.Manifest
	if m.Name != "docs-site" || m.Form != "static" || m.Root != "web/dist" || m.Build != "cd 'web' && npm run build" {
		t.Fatalf("manifest = %+v", m.Component)
	}
	if got := m.Env["API_BASE"]; got != "https://api.example.com" {
		t.Fatalf("API_BASE = %q", got)
	}
}

func TestRenderNodeWebImporterGeneratesDockerfile(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "render.yaml"), `services:
  - type: web
    name: api
    runtime: node
    buildCommand: npm run build
    startCommand: node server.js
    envVars:
      - key: PORT
        value: "3333"
      - key: SESSION_SECRET
        generateValue: true
`)
	res, err := Render(filepath.Join(dir, "render.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Manifest.Form != "web-service" || res.Manifest.Port != 3333 {
		t.Fatalf("manifest = %+v", res.Manifest.Component)
	}
	if _, ok := res.ExtraFiles["Dockerfile"]; !ok {
		t.Fatalf("expected generated Dockerfile")
	}
	if !strings.Contains(string(res.ExtraFiles["Dockerfile"]), `CMD ["sh", "-lc", "node server.js"]`) {
		t.Fatalf("Dockerfile did not preserve start command:\n%s", res.ExtraFiles["Dockerfile"])
	}
	if len(res.Warnings) == 0 || !strings.Contains(strings.Join(res.Warnings, "\n"), "SESSION_SECRET") {
		t.Fatalf("expected secret warning, got %#v", res.Warnings)
	}
}

func TestVercelStaticImporter(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "vercel.json"), `{
  "name": "marketing-site",
  "framework": "vite",
  "buildCommand": "pnpm build",
  "outputDirectory": "dist",
  "env": {"PUBLIC_API": "https://api.example.com"},
  "redirects": [{"source": "/old", "destination": "/new"}]
}`)
	res, err := Vercel(dir)
	if err != nil {
		t.Fatal(err)
	}
	m := res.Manifest
	if m.Name != "marketing-site" || m.Form != "static" || m.Root != "dist" || m.Build != "pnpm build" {
		t.Fatalf("manifest = %+v", m.Component)
	}
	if got := m.Env["PUBLIC_API"]; got != "https://api.example.com" {
		t.Fatalf("PUBLIC_API = %q", got)
	}
	if !strings.Contains(strings.Join(res.Warnings, "\n"), "redirects dropped") {
		t.Fatalf("expected redirect warning, got %#v", res.Warnings)
	}
}

func TestNixFlakeImporterGeneratesDockerfile(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "flake.nix"), `{
  description = "Tiny Nix Web";
  outputs = { self, nixpkgs }: {
    packages.x86_64-linux.default = nixpkgs.legacyPackages.x86_64-linux.writeShellApplication {
      name = "tiny-nix-web";
      text = "echo hello";
    };
  };
}`)
	res, err := NixFlake(dir)
	if err != nil {
		t.Fatal(err)
	}
	if res.Manifest.Name != "tiny-nix-web" || res.Manifest.Form != "web-service" || res.Manifest.Port != 8080 {
		t.Fatalf("manifest = %+v", res.Manifest.Component)
	}
	if !strings.Contains(string(res.ExtraFiles["Dockerfile"]), "nix-command flakes") {
		t.Fatalf("unexpected Dockerfile:\n%s", res.ExtraFiles["Dockerfile"])
	}
}

func TestHelmImporterDeploymentServiceIngress(t *testing.T) {
	withFakeHelm(t, `apiVersion: apps/v1
kind: Deployment
metadata:
  name: helm-demo
spec:
  replicas: 2
  selector:
    matchLabels:
      app: helm-demo
  template:
    metadata:
      labels:
        app: helm-demo
    spec:
      containers:
        - name: web
          image: nginx:alpine
          ports:
            - name: http
              containerPort: 8080
          env:
            - name: GREETING
              value: hello
          resources:
            requests:
              cpu: 250m
              memory: 128Mi
          readinessProbe:
            httpGet:
              path: /
              port: http
        - name: metrics
          image: prom/statsd-exporter:v0.27.1
---
apiVersion: v1
kind: Service
metadata:
  name: helm-demo
spec:
  type: LoadBalancer
  selector:
    app: helm-demo
  ports:
    - name: http
      port: 80
      targetPort: http
---
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: helm-demo
spec:
  tls:
    - hosts: [helm.example.com]
      secretName: helm-tls
  rules:
    - host: helm.example.com
      http:
        paths:
          - path: /
            pathType: Prefix
            backend:
              service:
                name: helm-demo
                port:
                  number: 80
`)
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "Chart.yaml"), "name: helm-demo\n")
	res, err := Helm(dir)
	if err != nil {
		t.Fatal(err)
	}
	m := res.Manifest
	if m.Name != "helm-demo" || m.Form != "web-service" || m.Port != 8080 || m.Replicas != 2 || m.Domain != "helm.example.com" {
		t.Fatalf("manifest = %+v", m.Component)
	}
	if m.CPU != 250 || m.Memory != 128 || m.Env["GREETING"] != "hello" || len(m.Sidecars) != 1 {
		t.Fatalf("component details = %+v", m.Component)
	}
	warnings := strings.Join(res.Warnings, "\n")
	for _, want := range []string{"type=LoadBalancer dropped", "TLS secret config dropped", "readinessProbe", "sidecars"} {
		if !strings.Contains(warnings, want) {
			t.Fatalf("warnings missing %q: %#v", want, res.Warnings)
		}
	}
}

func TestHelmImporterCronJob(t *testing.T) {
	withFakeHelm(t, `apiVersion: batch/v1
kind: CronJob
metadata:
  name: cleanup
spec:
  schedule: "*/5 * * * *"
  jobTemplate:
    spec:
      template:
        spec:
          restartPolicy: OnFailure
          containers:
            - name: cleanup
              image: alpine:3.21
              command: ["sh", "-c", "echo ok"]
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: cleanup-config
`)
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "Chart.yaml"), "name: cleanup\n")
	res, err := Helm(dir)
	if err != nil {
		t.Fatal(err)
	}
	if res.Manifest.Form != "cronjob" || res.Manifest.Schedule != "*/5 * * * *" || res.Manifest.Image != "alpine:3.21" {
		t.Fatalf("manifest = %+v", res.Manifest.Component)
	}
	if !strings.Contains(strings.Join(res.Warnings, "\n"), "ConfigMap/cleanup-config dropped") {
		t.Fatalf("expected ConfigMap warning, got %#v", res.Warnings)
	}
}

func TestKubernetesImporterDirectory(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "deployment.yaml"), `apiVersion: apps/v1
kind: Deployment
metadata:
  name: kube-demo
spec:
  replicas: 2
  template:
    metadata:
      labels:
        app: kube-demo
    spec:
      containers:
        - name: web
          image: nginx:alpine
          ports:
            - name: http
              containerPort: 8080
`)
	writeTestFile(t, filepath.Join(dir, "service.yaml"), `apiVersion: v1
kind: Service
metadata:
  name: kube-demo
spec:
  selector:
    app: kube-demo
  ports:
    - name: http
      port: 80
      targetPort: http
`)
	writeTestFile(t, filepath.Join(dir, "ingress.yaml"), `apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: kube-demo
spec:
  rules:
    - host: kube.example.com
      http:
        paths:
          - path: /
            pathType: Prefix
            backend:
              service:
                name: kube-demo
                port:
                  name: http
`)
	writeTestFile(t, filepath.Join(dir, "secret.yaml"), `apiVersion: v1
kind: Secret
metadata:
  name: kube-demo-secret
`)
	res, err := Kubernetes(dir)
	if err != nil {
		t.Fatal(err)
	}
	m := res.Manifest
	if res.Source != "kubernetes" || m.Name != "kube-demo" || m.Form != "web-service" || m.Port != 8080 || m.Replicas != 2 || m.Domain != "kube.example.com" {
		t.Fatalf("manifest = %+v", m.Component)
	}
	if !strings.Contains(strings.Join(res.Warnings, "\n"), "Secret/kube-demo-secret dropped") {
		t.Fatalf("expected secret warning, got %#v", res.Warnings)
	}
}

func TestKubernetesImporterFileCronJob(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cleanup.yaml")
	writeTestFile(t, path, `apiVersion: batch/v1
kind: CronJob
metadata:
  name: cleanup
spec:
  schedule: "*/10 * * * *"
  jobTemplate:
    spec:
      template:
        spec:
          containers:
            - name: cleanup
              image: alpine:3.21
              args: ["sh", "-c", "echo ok"]
`)
	res, err := Kubernetes(path)
	if err != nil {
		t.Fatal(err)
	}
	if res.Manifest.Form != "cronjob" || res.Manifest.Schedule != "*/10 * * * *" || res.Manifest.Image != "alpine:3.21" || strings.Join(res.Manifest.Command, " ") != "sh -c echo ok" {
		t.Fatalf("manifest = %+v", res.Manifest.Component)
	}
}

func withFakeHelm(t *testing.T, rendered string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake helm shell script requires sh")
	}
	dir := t.TempDir()
	helmPath := filepath.Join(dir, "helm")
	body := "#!/bin/sh\nif [ \"$1\" != template ]; then echo unexpected helm args >&2; exit 2; fi\ncat <<'EOF'\n" + rendered + "\nEOF\n"
	if err := os.WriteFile(helmPath, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func writeTestFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
