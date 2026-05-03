package importers

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/darvell/blob/internal/manifest"
)

// Procfile parses a Heroku-style Procfile at path. Each line is
// `<process_type>: <command>`. A `web:` process becomes a web-service
// component (port 8080 unless PORT is referenced); other types become
// daemons. A `release:` process — Heroku's pre-deploy hook — becomes a
// `job` component which the operator must run manually after deploy
// (we warn).
//
// The directory containing the Procfile must also have a Dockerfile
// or a compatible buildpack-rendered image; we don't run buildpacks
// ourselves. We surface this requirement as a warning when no
// Dockerfile is present.
func Procfile(path string) (*Result, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	res := &Result{Source: "procfile"}
	dir := filepath.Dir(path)
	projectName := sanitizeName(filepath.Base(dir))

	type proc struct {
		name string
		cmd  []string
	}
	var procs []proc
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		colon := strings.Index(line, ":")
		if colon <= 0 {
			res.Warnings = append(res.Warnings, fmt.Sprintf("skipped malformed line: %q", line))
			continue
		}
		name := strings.TrimSpace(line[:colon])
		cmd := strings.TrimSpace(line[colon+1:])
		if name == "" || cmd == "" {
			continue
		}
		procs = append(procs, proc{name: sanitizeName(name), cmd: parseShellCmd(cmd)})
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if len(procs) == 0 {
		return nil, fmt.Errorf("no processes in Procfile")
	}

	if !exists(dir, "Dockerfile") {
		res.Warnings = append(res.Warnings,
			"no Dockerfile in the Procfile directory — blob does not run buildpacks. Add a Dockerfile (heroku/heroku:24 or any base) before `blob deploy`.")
	}

	// Stable ordering: web first, release last, others in between alphabetically.
	sort.SliceStable(procs, func(i, j int) bool {
		ri, rj := rank(procs[i].name), rank(procs[j].name)
		if ri != rj {
			return ri < rj
		}
		return procs[i].name < procs[j].name
	})

	var components []manifest.Component
	for _, p := range procs {
		c := manifest.Component{
			Name:    p.name,
			Command: p.cmd,
		}
		switch p.name {
		case "web":
			c.Form = "web-service"
			c.Port = 8080
			res.Warnings = append(res.Warnings,
				"web process: defaulted to port 8080. If your app reads $PORT, ensure it binds to 8080 (we don't rewrite the env).")
		case "release":
			c.Form = "job"
			res.Warnings = append(res.Warnings,
				"release process imported as form: job. Heroku auto-runs release tasks before each deploy; blob does not. Run it manually with `blob exec` after each deploy, or migrate the logic into your Dockerfile/entrypoint.")
		default:
			c.Form = "daemon"
		}
		components = append(components, c)
	}

	m := &manifest.Manifest{}
	if len(components) == 1 {
		m.Component = components[0]
		if m.Component.Name != "web" {
			m.Component.Name = projectName
		} else {
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

func rank(name string) int {
	switch name {
	case "web":
		return 0
	case "release":
		return 9
	default:
		return 5
	}
}

func parseShellCmd(cmd string) []string {
	// We don't try to parse a real shell — just split on whitespace. The
	// vast majority of Procfile commands are `bin/start`, `node server.js`,
	// `gunicorn app:wsgi`. If the line uses shell features (pipes, &&),
	// we wrap it in `sh -c`.
	if strings.ContainsAny(cmd, "|&;<>$`") {
		return []string{"sh", "-c", cmd}
	}
	return strings.Fields(cmd)
}

func exists(dir, name string) bool {
	_, err := os.Stat(filepath.Join(dir, name))
	return err == nil
}
