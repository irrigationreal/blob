package importers

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/darvell/blob/internal/manifest"
)

// NixFlake imports a flake.nix project. It emits a web-service manifest and a
// Dockerfile that builds the flake's default package with Nix, then runs the
// first executable found under result/bin. Operators can edit the generated
// command/form for daemons or packages that expose a different entrypoint.
func NixFlake(path string) (*Result, error) {
	path, err := flakePath(path)
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	src := string(b)
	dir := filepath.Dir(path)
	name := sanitizeName(firstRegex(src,
		`(?m)\bpname\s*=\s*"([^"]+)"`,
		`(?m)\bname\s*=\s*"([^"]+)"`,
		`(?m)\bdescription\s*=\s*"([^"]+)"`,
	))
	if name == "" {
		name = sanitizeName(filepath.Base(dir))
	}
	port := 8080
	if p := firstRegex(src, `(?m)\bPORT\s*=\s*"?([0-9]+)"?`); p != "" {
		if n := parsePort(p); n > 0 {
			port = n
		}
	}

	res := &Result{
		Source: "nix",
		Manifest: &manifest.Manifest{Component: manifest.Component{
			Name: name,
			Form: "web-service",
			Port: port,
			Env:  map[string]string{"PORT": fmt.Sprint(port)},
		}},
		ExtraFiles: map[string][]byte{
			"Dockerfile":    []byte(nixDockerfile(port)),
			".dockerignore": []byte(nixDockerignore),
		},
	}
	res.Warnings = append(res.Warnings,
		"generated Dockerfile expects `nix build` to produce an executable under result/bin; edit Dockerfile/CMD if this flake exposes only apps or devShells",
	)
	if strings.Contains(src, "nixosConfigurations") {
		res.Warnings = append(res.Warnings, "nixosConfigurations detected -host/system configs are not imported; only the default package deploy path was generated")
	}
	if strings.Contains(src, "devShell") || strings.Contains(src, "devShells") {
		res.Warnings = append(res.Warnings, "devShell outputs ignored -Blob deploys a built package, not a development shell")
	}
	if err := res.Render(); err != nil {
		return nil, err
	}
	return res, nil
}

func flakePath(path string) (string, error) {
	if fi, err := os.Stat(path); err == nil && fi.IsDir() {
		p := filepath.Join(path, "flake.nix")
		if _, err := os.Stat(p); err != nil {
			return "", fmt.Errorf("no flake.nix in %s", path)
		}
		return p, nil
	}
	return path, nil
}

func firstRegex(src string, patterns ...string) string {
	for _, pattern := range patterns {
		if m := regexp.MustCompile(pattern).FindStringSubmatch(src); len(m) == 2 {
			return strings.TrimSpace(m[1])
		}
	}
	return ""
}

func nixDockerfile(port int) string {
	return fmt.Sprintf(`FROM nixos/nix:2.24.11
WORKDIR /app
COPY . .
RUN nix --extra-experimental-features "nix-command flakes" build
ENV PORT=%d
EXPOSE %d
CMD ["sh", "-lc", "bin=$(find -L ./result/bin -maxdepth 1 -type f -perm -111 | sort | head -n1); if [ -z \"$bin\" ]; then echo 'no executable found under ./result/bin' >&2; exit 1; fi; exec \"$bin\""]
`, port, port)
}

const nixDockerignore = `.git
result
.direnv
.env*
.DS_Store
`
