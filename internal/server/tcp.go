package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/irrigationreal/blob/internal/api"
)

const (
	tcpPortFloor = 20000
	tcpPortCeil  = 20100
)

func (s *Server) tcpDir() string {
	return filepath.Join(s.cfg.StateDir, "tcp")
}

func (s *Server) tcpBindingPath(publicPort int) string {
	return filepath.Join(s.tcpDir(), strconv.Itoa(publicPort)+".json")
}

func tcpEntrypoint(publicPort int) string {
	return fmt.Sprintf("blobtcp%d", publicPort)
}

func tcpRouterName(app string, publicPort int) string {
	return fmt.Sprintf("blobtcp-%s-%d", app, publicPort)
}

func (s *Server) handleTCP(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "GET":
		out, err := s.listTCPBindings()
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, out)
	case "POST":
		var req api.AddTCPRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		out, err := s.addTCPBinding(r.Context(), &req)
		if err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		writeJSON(w, 200, out)
	default:
		writeErr(w, 405, "method not allowed")
	}
}

func (s *Server) handleTCPItem(w http.ResponseWriter, r *http.Request) {
	port, err := strconv.Atoi(strings.Trim(strings.TrimPrefix(r.URL.Path, "/v1/tcp/"), "/"))
	if err != nil || !validTCPPublicPort(port) {
		writeErr(w, 400, "invalid public tcp port")
		return
	}
	switch r.Method {
	case "GET":
		b, err := s.loadTCPBinding(port)
		if err != nil {
			writeErr(w, 404, "tcp binding not found")
			return
		}
		writeJSON(w, 200, b)
	case "DELETE":
		if err := s.removeTCPBinding(r.Context(), port); err != nil {
			writeErr(w, 404, err.Error())
			return
		}
		writeJSON(w, 200, map[string]any{"public_port": port, "removed": true})
	default:
		writeErr(w, 405, "method not allowed")
	}
}

func validTCPPublicPort(port int) bool {
	return port >= tcpPortFloor && port < tcpPortCeil
}

func (s *Server) addTCPBinding(ctx context.Context, req *api.AddTCPRequest) (*api.AddTCPResponse, error) {
	app := strings.TrimSpace(req.App)
	if !validName(app) {
		return nil, errors.New("invalid app name")
	}
	meta, ok := s.loadJobMeta(app)
	if !ok {
		return nil, errors.New("app metadata not found; deploy the app first")
	}
	if meta.Exposure != "tcp" {
		return nil, errors.New("app is not deployed with exposure: tcp")
	}
	targetPort := req.TargetPort
	if targetPort <= 0 {
		targetPort = meta.Port
	}
	if targetPort <= 0 || targetPort > 65535 {
		return nil, errors.New("target_port must be 1-65535; set port: in blob.yaml")
	}
	if existing := s.tcpBindingForApp(app); existing != nil {
		return &api.AddTCPResponse{Binding: *existing, Note: "app already has a public TCP binding"}, nil
	}
	publicPort := req.PublicPort
	if publicPort == 0 {
		p, err := s.allocateTCPPort()
		if err != nil {
			return nil, err
		}
		publicPort = p
	} else if !validTCPPublicPort(publicPort) {
		return nil, fmt.Errorf("public_port must be between %d and %d", tcpPortFloor, tcpPortCeil-1)
	} else if _, err := s.loadTCPBinding(publicPort); err == nil {
		return nil, fmt.Errorf("public_port %d is already allocated", publicPort)
	}
	b := &api.TCPBinding{
		App:        app,
		Host:       s.postgresHost(),
		PublicPort: publicPort,
		TargetPort: targetPort,
		Entrypoint: tcpEntrypoint(publicPort),
		URL:        fmt.Sprintf("tcp://%s:%d", s.postgresHost(), publicPort),
		CreatedAt:  time.Now().UTC(),
	}
	if err := s.saveTCPBinding(b); err != nil {
		return nil, err
	}
	if err := s.ensureTraefikTCPEntrypoints(ctx); err != nil {
		_ = s.deleteTCPBinding(publicPort)
		return nil, err
	}
	if err := s.addTCPTagsToApp(ctx, b); err != nil {
		_ = s.deleteTCPBinding(publicPort)
		_ = s.ensureTraefikTCPEntrypoints(ctx)
		return nil, err
	}
	return &api.AddTCPResponse{Binding: *b, Note: fmt.Sprintf("ensure the platform firewall allows tcp/%d", publicPort)}, nil
}

func (s *Server) allocateTCPPort() (int, error) {
	bindings, err := s.loadAllTCPBindings()
	if err != nil {
		return 0, err
	}
	used := map[int]bool{}
	for _, b := range bindings {
		used[b.PublicPort] = true
	}
	for p := tcpPortFloor; p < tcpPortCeil; p++ {
		if !used[p] {
			return p, nil
		}
	}
	return 0, errors.New("no tcp public ports available")
}

func (s *Server) listTCPBindings() (*api.ListTCPResponse, error) {
	bindings, err := s.loadAllTCPBindings()
	if err != nil {
		return nil, err
	}
	return &api.ListTCPResponse{Bindings: bindings}, nil
}

func (s *Server) loadAllTCPBindings() ([]api.TCPBinding, error) {
	entries, err := os.ReadDir(s.tcpDir())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	out := make([]api.TCPBinding, 0, len(entries))
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		port, err := strconv.Atoi(strings.TrimSuffix(e.Name(), ".json"))
		if err != nil {
			continue
		}
		b, err := s.loadTCPBinding(port)
		if err != nil {
			continue
		}
		out = append(out, *b)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].PublicPort < out[j].PublicPort })
	return out, nil
}

func (s *Server) tcpBindingForApp(app string) *api.TCPBinding {
	bindings, err := s.loadAllTCPBindings()
	if err != nil {
		return nil
	}
	for _, b := range bindings {
		if b.App == app {
			bb := b
			return &bb
		}
	}
	return nil
}

func (s *Server) tcpBindingsForApp(app string) []api.TCPBinding {
	bindings, err := s.loadAllTCPBindings()
	if err != nil {
		return nil
	}
	out := make([]api.TCPBinding, 0, 1)
	for _, b := range bindings {
		if b.App == app {
			out = append(out, b)
		}
	}
	return out
}

func (s *Server) loadTCPBinding(publicPort int) (*api.TCPBinding, error) {
	b, err := os.ReadFile(s.tcpBindingPath(publicPort))
	if err != nil {
		return nil, err
	}
	binding := &api.TCPBinding{}
	if err := json.Unmarshal(b, binding); err != nil {
		return nil, err
	}
	if binding.Entrypoint == "" {
		binding.Entrypoint = tcpEntrypoint(binding.PublicPort)
	}
	if binding.URL == "" && binding.Host != "" {
		binding.URL = fmt.Sprintf("tcp://%s:%d", binding.Host, binding.PublicPort)
	}
	return binding, nil
}

func (s *Server) saveTCPBinding(binding *api.TCPBinding) error {
	if err := os.MkdirAll(s.tcpDir(), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(binding, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.tcpBindingPath(binding.PublicPort), b, 0o600)
}

func (s *Server) deleteTCPBinding(publicPort int) error {
	return removeIgnoringMissing(s.tcpBindingPath(publicPort))
}

func (s *Server) removeTCPBinding(ctx context.Context, publicPort int) error {
	binding, err := s.loadTCPBinding(publicPort)
	if err != nil {
		return errors.New("tcp binding not found")
	}
	jobPath := filepath.Join(s.cfg.JobsDir, binding.App+".nomad")
	if existing, err := readFile(jobPath); err == nil {
		updated := removeTCPTagsFromJob(existing, *binding)
		if updated != existing {
			if err := writeFileAtomic(jobPath, []byte(updated)); err != nil {
				return err
			}
			if err := s.run(ctx, "nomad", "job", "run", jobPath); err != nil {
				return err
			}
		}
	}
	if err := s.deleteTCPBinding(publicPort); err != nil {
		return err
	}
	return s.ensureTraefikTCPEntrypoints(ctx)
}

func (s *Server) addExistingTCPBindingsToJob(app, hcl string) (string, error) {
	for _, binding := range s.tcpBindingsForApp(app) {
		updated, err := addTCPTagsToJob(hcl, binding)
		if err != nil {
			return "", err
		}
		hcl = updated
	}
	return hcl, nil
}

func (s *Server) addTCPTagsToApp(ctx context.Context, binding *api.TCPBinding) error {
	jobPath := filepath.Join(s.cfg.JobsDir, binding.App+".nomad")
	existing, err := readFile(jobPath)
	if err != nil {
		return fmt.Errorf("read app job: %w", err)
	}
	updated, err := addTCPTagsToJob(existing, *binding)
	if err != nil {
		return err
	}
	if updated == existing {
		return nil
	}
	if err := writeFileAtomic(jobPath, []byte(updated)); err != nil {
		return err
	}
	return s.run(ctx, "nomad", "job", "run", jobPath)
}

func addTCPTagsToJob(hcl string, binding api.TCPBinding) (string, error) {
	router := tcpRouterName(binding.App, binding.PublicPort)
	if strings.Contains(hcl, "traefik.enable=true") || strings.Contains(hcl, "traefik.tcp.routers."+router+".") || strings.Contains(hcl, "traefik.tcp.services."+router+".") {
		hcl = removeTCPTagsFromJob(hcl, binding)
	}
	insertAt := tcpServiceTagsInsertIndex(hcl, binding.App)
	if insertAt < 0 {
		return "", errors.New("tcp-exposed app job has no service tags block; redeploy with exposure: tcp")
	}
	return hcl[:insertAt] + renderTCPRouterTags(binding) + hcl[insertAt:], nil
}

func tcpServiceTagsInsertIndex(hcl, app string) int {
	serviceNeedle := fmt.Sprintf("service {\n      name     = %q", app)
	serviceAt := strings.Index(hcl, serviceNeedle)
	if serviceAt < 0 {
		return -1
	}
	tagsNeedle := "tags = [\n"
	tagsAt := strings.Index(hcl[serviceAt:], tagsNeedle)
	if tagsAt < 0 {
		return -1
	}
	return serviceAt + tagsAt + len(tagsNeedle)
}

func renderTCPRouterTags(binding api.TCPBinding) string {
	router := tcpRouterName(binding.App, binding.PublicPort)
	return fmt.Sprintf("        \"traefik.enable=true\",\n        \"traefik.tcp.routers.%s.entrypoints=%s\",\n        \"traefik.tcp.routers.%s.rule=HostSNI(`*`)\",\n        \"traefik.tcp.routers.%s.service=%s\",\n        \"traefik.tcp.services.%s.loadbalancer.server.tls=false\",\n", router, binding.Entrypoint, router, router, router, router)
}

func removeTCPTagsFromJob(hcl string, binding api.TCPBinding) string {
	router := tcpRouterName(binding.App, binding.PublicPort)
	var out strings.Builder
	for _, line := range strings.SplitAfter(hcl, "\n") {
		if strings.Contains(line, "traefik.enable=true") || strings.Contains(line, "traefik.tcp.routers."+router+".") || strings.Contains(line, "traefik.tcp.services."+router+".") {
			continue
		}
		out.WriteString(line)
	}
	return out.String()
}

func (s *Server) ensureTraefikTCPEntrypoints(ctx context.Context) error {
	bindings, err := s.loadAllTCPBindings()
	if err != nil {
		return err
	}
	ports := make([]int, 0, len(bindings))
	for _, b := range bindings {
		if validTCPPublicPort(b.PublicPort) {
			ports = append(ports, b.PublicPort)
		}
	}
	if err := os.MkdirAll(s.cfg.JobsDir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(s.cfg.JobsDir, "edge-traefik.nomad")
	hcl, err := readFile(path)
	if err == nil {
		patched, err := patchEdgeTraefikTCPEntrypoints(hcl, ports)
		if err != nil {
			return err
		}
		if patched == hcl {
			return nil
		}
		if err := writeFileAtomic(path, []byte(patched)); err != nil {
			return err
		}
		return s.run(ctx, "nomad", "job", "run", path)
	}
	email := s.traefikACMEEmail(ctx)
	if email == "" {
		email = "admin@" + strings.Trim(s.cfg.BaseDomain, ".")
	}
	if email == "admin@" {
		email = "admin@example.com"
	}
	if err := os.WriteFile(path, []byte(renderEdgeTraefikJob(s.cfg.Datacenter, email, ports)), 0o644); err != nil {
		return err
	}
	return s.run(ctx, "nomad", "job", "run", path)
}

func (s *Server) traefikACMEEmail(ctx context.Context) string {
	body, err := s.nomadGET(ctx, "/v1/job/edge-traefik")
	if err != nil {
		return ""
	}
	var job struct {
		TaskGroups []struct {
			Tasks []struct {
				Config map[string]any `json:"Config"`
			} `json:"Tasks"`
		} `json:"TaskGroups"`
	}
	if err := json.Unmarshal(body, &job); err != nil {
		return ""
	}
	for _, group := range job.TaskGroups {
		for _, task := range group.Tasks {
			args, ok := task.Config["args"].([]any)
			if !ok {
				continue
			}
			for _, arg := range args {
				s, ok := arg.(string)
				if !ok {
					continue
				}
				if email, ok := strings.CutPrefix(s, "--certificatesresolvers.le.acme.email="); ok {
					return email
				}
			}
		}
	}
	return ""
}

func normalizedTCPPorts(ports []int) []int {
	ports = append([]int(nil), ports...)
	sort.Ints(ports)
	uniq := ports[:0]
	last := -1
	for _, p := range ports {
		if p == last || !validTCPPublicPort(p) {
			continue
		}
		uniq = append(uniq, p)
		last = p
	}
	return uniq
}

func renderEdgeTCPNetworkLines(ports []int) string {
	var out strings.Builder
	for _, p := range normalizedTCPPorts(ports) {
		fmt.Fprintf(&out, "      port \"tcp%d\" { static = %d }\n", p, p)
	}
	return out.String()
}

func renderEdgeTCPArgLines(ports []int) string {
	var out strings.Builder
	for _, p := range normalizedTCPPorts(ports) {
		fmt.Fprintf(&out, "          %q,\n", fmt.Sprintf("--entrypoints.%s.address=:%d", tcpEntrypoint(p), p))
	}
	return out.String()
}

func isBlobTCPPortLine(line string) bool {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "port \"tcp") || !strings.Contains(line, "static =") {
		return false
	}
	nameEnd := strings.Index(line[len("port \"tcp"):], "\"")
	if nameEnd < 0 {
		return false
	}
	port, err := strconv.Atoi(line[len("port \"tcp") : len("port \"tcp")+nameEnd])
	return err == nil && validTCPPublicPort(port)
}

func patchEdgeTraefikTCPEntrypoints(hcl string, ports []int) (string, error) {
	networkLines := renderEdgeTCPNetworkLines(ports)
	argLines := renderEdgeTCPArgLines(ports)
	lines := strings.SplitAfter(hcl, "\n")
	var out strings.Builder
	inNetwork := false
	inArgs := false
	networkInserted := networkLines == ""
	argsInserted := argLines == ""
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.Contains(line, "network {") {
			inNetwork = true
		}
		if inNetwork && isBlobTCPPortLine(line) {
			continue
		}
		if strings.Contains(line, "--entrypoints.blobtcp") {
			continue
		}
		if strings.Contains(line, "args = [") {
			inArgs = true
		}
		if inNetwork && trimmed == "}" && !networkInserted {
			out.WriteString(networkLines)
			networkInserted = true
		}
		if inArgs && trimmed == "]" && !argsInserted {
			out.WriteString(argLines)
			argsInserted = true
		}
		out.WriteString(line)
		if inNetwork && !networkInserted && strings.Contains(line, `port "https"`) {
			out.WriteString(networkLines)
			networkInserted = true
		}
		if inNetwork && trimmed == "}" {
			inNetwork = false
		}
		if inArgs && !argsInserted && strings.Contains(line, "--entrypoints.websecure.address=:443") {
			out.WriteString(argLines)
			argsInserted = true
		}
		if inArgs && trimmed == "]" {
			inArgs = false
		}
	}
	if !networkInserted {
		return "", errors.New("edge-traefik job has no network block to patch")
	}
	if !argsInserted {
		return "", errors.New("edge-traefik job has no traefik args block to patch")
	}
	return out.String(), nil
}

func renderEdgeTraefikJob(dc, email string, ports []int) string {
	uniq := normalizedTCPPorts(ports)
	var network strings.Builder
	network.WriteString("      port \"http\"  { static = 80 }\n")
	network.WriteString("      port \"https\" { static = 443 }\n")
	var args strings.Builder
	baseArgs := []string{
		"--log.level=INFO",
		"--api.dashboard=false",
		"--entrypoints.web.address=:80",
		"--entrypoints.websecure.address=:443",
		"--providers.nomad=true",
		"--providers.nomad.endpoint.address=http://127.0.0.1:4646",
		"--providers.nomad.exposedbydefault=false",
		"--certificatesresolvers.le.acme.email=" + email,
		"--certificatesresolvers.le.acme.storage=/acme.json",
		"--certificatesresolvers.le.acme.httpchallenge=true",
		"--certificatesresolvers.le.acme.httpchallenge.entrypoint=web",
	}
	for _, arg := range baseArgs {
		fmt.Fprintf(&args, "          %q,\n", arg)
	}
	for _, p := range uniq {
		fmt.Fprintf(&network, "      port \"tcp%d\" { static = %d }\n", p, p)
		fmt.Fprintf(&args, "          %q,\n", fmt.Sprintf("--entrypoints.%s.address=:%d", tcpEntrypoint(p), p))
	}
	return fmt.Sprintf(`job "edge-traefik" {
  datacenters = [%q]
  type = "service"
  group "edge" {
    count = 1
    network {
%s    }
    task "traefik" {
      driver = "docker"
      config {
        image        = "traefik:v3.6"
        network_mode = "host"
        volumes = ["/srv/traefik/acme.json:/acme.json"]
        args = [
%s        ]
      }
      resources {
        cpu    = 200
        memory = 256
      }
    }
  }
}
`, dc, network.String(), args.String())
}
