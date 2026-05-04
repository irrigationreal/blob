package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/darvell/blob/internal/api"
)

// --- nodes -------------------------------------------------------------------

func (s *Server) handleNodes(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		writeErr(w, 405, "method not allowed")
		return
	}
	out, err := s.listNodes(r.Context())
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, out)
}

func (s *Server) handleNodeItem(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/v1/nodes/")
	parts := strings.SplitN(rest, "/", 2)
	id := parts[0]
	if id == "" {
		writeErr(w, 400, "node id required")
		return
	}
	switch {
	case len(parts) == 2 && parts[1] == "drain" && r.Method == "POST":
		if err := s.drainNode(r.Context(), id, true); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, map[string]any{"id": id, "drain": true})
	case len(parts) == 2 && parts[1] == "drain" && r.Method == "DELETE":
		if err := s.drainNode(r.Context(), id, false); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, map[string]any{"id": id, "drain": false})
	default:
		writeErr(w, 404, "not found")
	}
}

func (s *Server) listNodes(ctx context.Context) (*api.ListNodesResponse, error) {
	body, err := s.nomadGET(ctx, "/v1/nodes")
	if err != nil {
		return nil, err
	}
	var raw []struct {
		ID, Name, Datacenter, Status, NodeClass string
		Address, HTTPAddr                       string
		Drivers                                 map[string]any
		SchedulingEligibility                   string
		Drain                                   bool
		Attributes                              map[string]string `json:"-"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, err
	}
	out := &api.ListNodesResponse{}
	for _, n := range raw {
		out.Nodes = append(out.Nodes, api.Node{
			ID:         n.ID,
			Name:       n.Name,
			Address:    n.Address,
			Datacenter: n.Datacenter,
			Status:     n.Status,
			Eligible:   n.SchedulingEligibility,
			Drain:      n.Drain,
			NodeClass:  n.NodeClass,
		})
	}
	sort.Slice(out.Nodes, func(i, j int) bool { return out.Nodes[i].Name < out.Nodes[j].Name })
	return out, nil
}

func (s *Server) drainNode(ctx context.Context, id string, on bool) error {
	if on {
		return s.run(ctx, "nomad", "node", "drain", "-enable", "-yes", id)
	}
	return s.run(ctx, "nomad", "node", "drain", "-disable", "-yes", id)
}

// --- join --------------------------------------------------------------------
//
// Generates a one-liner shell script the operator runs on a new machine to
// install Docker + Nomad client and join the existing cluster. The cluster
// address is computed from the host the API is reachable on (headerless: we
// trust the configured external address); for v1, we read it from the
// optional BLOB_FLEET_JOIN_ADDR env (fall back to the local Nomad's
// http_addr).

func (s *Server) handleJoin(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" && r.Method != "GET" {
		writeErr(w, 405, "method not allowed")
		return
	}
	addr, err := s.nomadServerAddr(r.Context())
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	// Bake registry pull credentials into the join script when available.
	// /v1/join is already auth-gated, so embedding the creds here doesn't
	// widen exposure compared with anyone who already has the bearer
	// token. Without this, the first deploy onto a new node fails the
	// pull with `unauthorized` and the operator has to docker-login
	// manually — which is what the old joining-nodes.md documented as a
	// "coming up" gap.
	regUser, regPass := "", ""
	if u, p, err := s.readRegistryCreds(); err == nil {
		regUser = u
		regPass = p
	}
	script := joinScript(addr, s.cfg.Datacenter, s.cfg.Registry, regUser, regPass)
	writeJSON(w, 200, api.JoinTokenResponse{Address: addr, Token: "", JoinScript: script})
}

func (s *Server) nomadServerAddr(ctx context.Context) (string, error) {
	body, err := s.nomadGET(ctx, "/v1/agent/self")
	if err != nil {
		return "", err
	}
	var self struct {
		Member struct {
			Addr string
			Tags map[string]string
		}
	}
	if err := json.Unmarshal(body, &self); err != nil {
		return "", err
	}
	if self.Member.Addr == "" {
		return "", errors.New("could not determine nomad server address")
	}
	port := self.Member.Tags["port"]
	if port == "" {
		port = "4647"
	}
	return self.Member.Addr + ":" + port, nil
}

func joinScript(serverRPC, dc, registry, regUser, regPass string) string {
	// A single shell script that installs Docker + Nomad client on a fresh
	// Debian/Ubuntu host and joins the existing cluster. The script is
	// idempotent.
	kataBlock := `
ENABLE_KATA=${ENABLE_KATA:-0}
KATA_VERSION=${KATA_VERSION:-3.30.0}
KATA_META=""
if [ "$ENABLE_KATA" = "1" ]; then
  echo "==> kata containers"
  if [ ! -e /dev/kvm ]; then
    echo "ENABLE_KATA=1 requires hardware virtualization exposed at /dev/kvm" >&2
    exit 1
  fi
  apt-get install -y zstd jq
  arch=$(dpkg --print-architecture)
  case "$arch" in
    amd64|arm64|ppc64le|s390x) kata_arch="$arch" ;;
    *) echo "unsupported Kata architecture: $arch" >&2; exit 1 ;;
  esac
  if [ ! -x /opt/kata/bin/kata-runtime ]; then
    url="https://github.com/kata-containers/kata-containers/releases/download/${KATA_VERSION}/kata-static-${KATA_VERSION}-${kata_arch}.tar.zst"
    tmp="/tmp/kata-static-${KATA_VERSION}-${kata_arch}.tar.zst"
    curl -fL "$url" -o "$tmp"
    tar --zstd -xf "$tmp" -C /
  fi
  mkdir -p /etc/docker
  if [ ! -s /etc/docker/daemon.json ]; then
    echo '{}' > /etc/docker/daemon.json
  fi
  tmp_json=$(mktemp)
  jq '.runtimes = (.runtimes // {}) | .runtimes["kata-runtime"] = {"runtimeType":"/opt/kata/bin/containerd-shim-kata-v2"}' \
    /etc/docker/daemon.json > "$tmp_json"
  mv "$tmp_json" /etc/docker/daemon.json
  systemctl restart docker || true
  /opt/kata/bin/kata-runtime check
  KATA_META='  meta {
    blob_kata = "true"
  }'
fi
`
	loginBlock := ""
	if registry != "" && regUser != "" && regPass != "" {
		// Pre-pull-time docker login so the first workload to schedule
		// here can pull from the private registry without manual setup.
		loginBlock = fmt.Sprintf(`
echo "==> docker login %s"
echo %q | docker login %s -u %q --password-stdin
`, registry, regPass, registry, regUser)
	}
	return fmt.Sprintf(`#!/bin/sh
# Generated by blobd. Run as root on a fresh Debian/Ubuntu node to join the Blob.
set -eu

echo "==> apt prereqs"
apt-get update
apt-get install -y ca-certificates curl gnupg lsb-release iproute2

if ! command -v docker >/dev/null; then
  install -m 0755 -d /etc/apt/keyrings
  curl -fsSL https://download.docker.com/linux/debian/gpg | gpg --dearmor -o /etc/apt/keyrings/docker.gpg
  chmod a+r /etc/apt/keyrings/docker.gpg
  . /etc/os-release
  echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.gpg] https://download.docker.com/linux/$ID $VERSION_CODENAME stable" | tee /etc/apt/sources.list.d/docker.list >/dev/null
  apt-get update
  apt-get install -y docker-ce docker-ce-cli containerd.io docker-compose-plugin
fi

if ! command -v nomad >/dev/null; then
  curl -fsSL https://apt.releases.hashicorp.com/gpg | gpg --dearmor -o /usr/share/keyrings/hashicorp-archive-keyring.gpg
  echo "deb [signed-by=/usr/share/keyrings/hashicorp-archive-keyring.gpg] https://apt.releases.hashicorp.com $(lsb_release -cs) main" | tee /etc/apt/sources.list.d/hashicorp.list
  apt-get update
  apt-get install -y nomad
fi
%s
mkdir -p /etc/nomad.d /opt/nomad/data
cat > /etc/nomad.d/client.hcl <<EOF
data_dir  = "/opt/nomad/data"
datacenter = "%s"
client {
  enabled = true
  servers = ["%s"]
$KATA_META
}
plugin "docker" {
  config {
    allow_privileged = false
    allow_runtimes   = ["runc", "kata-runtime"]
  }
}
EOF

systemctl enable --now docker
systemctl enable --now nomad
%s
echo "blob node up. Nomad will register this client with the server within a few seconds."
`, kataBlock, dc, serverRPC, loginBlock)
}

// --- volumes -----------------------------------------------------------------

func (s *Server) handleVolumes(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		writeErr(w, 405, "method not allowed")
		return
	}
	cmd := exec.CommandContext(r.Context(), "docker", "volume", "ls", "--format", "{{json .}}")
	out, err := cmd.Output()
	if err != nil {
		writeErr(w, 500, "list docker volumes: "+err.Error())
		return
	}
	resp := &api.ListVolumesResponse{}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var v struct {
			Name string
		}
		if err := json.Unmarshal([]byte(line), &v); err != nil {
			continue
		}
		// Convention: blob-<app>-<volname>
		if !strings.HasPrefix(v.Name, "blob-") {
			continue
		}
		rest := strings.TrimPrefix(v.Name, "blob-")
		i := strings.LastIndex(rest, "-")
		if i <= 0 {
			continue
		}
		app := rest[:i]
		vol := rest[i+1:]
		resp.Volumes = append(resp.Volumes, api.Volume{
			Name:     vol,
			App:      app,
			HostName: v.Name,
		})
	}
	writeJSON(w, 200, resp)
}

// --- restart / releases / exec / domains -------------------------------------

func (s *Server) restartApp(ctx context.Context, app string) error {
	// The cleanest restart is a Nomad job allocation restart. For batch jobs
	// (cronjobs) we re-run the periodic now via "force-eval".
	return s.run(ctx, "nomad", "job", "restart", "-yes", app)
}

func (s *Server) appReleases(ctx context.Context, app string) (*api.ListReleasesResponse, error) {
	body, err := s.nomadGET(ctx, "/v1/job/"+app+"/versions")
	if err != nil {
		return nil, err
	}
	var raw struct {
		Versions []struct {
			Version    int
			Stable     bool
			SubmitTime int64
			TaskGroups []struct {
				Tasks []struct {
					Config struct {
						Image string
					}
				}
			}
		}
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, err
	}
	resp := &api.ListReleasesResponse{}
	for _, v := range raw.Versions {
		image := ""
		if len(v.TaskGroups) > 0 && len(v.TaskGroups[0].Tasks) > 0 {
			image = v.TaskGroups[0].Tasks[0].Config.Image
		}
		resp.Releases = append(resp.Releases, api.Release{
			Revision:  v.Version,
			JobID:     app,
			Image:     image,
			Status:    fmt.Sprintf("v%d", v.Version),
			CreatedAt: time.Unix(0, v.SubmitTime),
		})
	}
	sort.Slice(resp.Releases, func(i, j int) bool { return resp.Releases[i].Revision > resp.Releases[j].Revision })
	return resp, nil
}

func (s *Server) appExec(ctx context.Context, app string, command []string) (*api.ExecResponse, error) {
	body, err := s.nomadGET(ctx, "/v1/job/"+app+"/allocations")
	if err != nil {
		return nil, err
	}
	var allocs []struct {
		ID, ClientStatus string
	}
	if err := json.Unmarshal(body, &allocs); err != nil {
		return nil, err
	}
	var allocID string
	for _, a := range allocs {
		if a.ClientStatus == "running" {
			allocID = a.ID
			break
		}
	}
	if allocID == "" {
		return nil, errors.New("no running allocation")
	}
	if len(command) == 0 {
		command = []string{"sh", "-c", "echo no command provided"}
	}
	args := append([]string{"alloc", "exec", "-i=false", "-t=false", "-task", "app", allocID}, command...)
	cmd := exec.CommandContext(ctx, "nomad", args...)
	out, err := cmd.CombinedOutput()
	resp := &api.ExecResponse{Output: string(out)}
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			resp.ExitCode = ee.ExitCode()
		} else {
			resp.ExitCode = 1
		}
	}
	return resp, nil
}

// attachDomain re-renders the Nomad job for an app with an additional Host
// in the Traefik router rule. Requires the app to have been deployed by
// blobd v0.3+ (so a meta.json exists).
func (s *Server) attachDomain(ctx context.Context, req *api.DomainAttachRequest) (*api.DomainAttachResponse, error) {
	meta, ok := s.loadJobMeta(req.App)
	if !ok {
		return nil, errors.New("no metadata for this app — re-deploy with the latest blobctl to enable domain attachment")
	}
	if !isHTTPForm(meta.Form) {
		return nil, errors.New("only web-service or static apps can have domains attached")
	}
	// Fetch the existing job, modify it, resubmit.
	body, err := s.nomadGET(ctx, "/v1/job/"+req.App)
	if err != nil {
		return nil, err
	}
	var job struct {
		TaskGroups []struct {
			Services []struct {
				Tags []string
			}
		}
	}
	_ = json.Unmarshal(body, &job)
	mode := req.Mode
	if mode == "" {
		mode = "user-external" // default for arbitrary user-supplied hostname
		if strings.HasSuffix(req.Host, "."+s.cfg.BaseDomain) || req.Host == s.cfg.BaseDomain {
			mode = "platform-base"
		}
	}
	// Re-render the job using its meta + the additional domain. We don't have
	// the original DeployRequest here; rebuild a minimal one from meta and
	// the running job's image. We trust meta to be enough for the routing
	// portion.
	newReq := &api.DeployRequest{
		App:         meta.App,
		Environment: meta.Environment,
		Domain:      meta.Domain,
		Domains:     []string{req.Host},
		Form:        meta.Form,
	}
	// Best-effort: keep CPU/memory/replicas from current job. For now we just
	// reuse defaults; a future iteration should fetch the running spec.
	newReq.CPU = 500
	newReq.Memory = 512
	newReq.Replicas = 1
	// Port is implicit for static (8080); for web-service we don't actually
	// need it for the Traefik routing alone — Nomad keeps the existing port
	// mapping alive on update if we re-submit with the same name. To avoid
	// breaking the running job we re-parse the existing rendered job file
	// off disk and add the host.
	id := req.App
	jobPath := joinPath(s.cfg.JobsDir, id+".nomad")
	if existing, err := readFile(jobPath); err == nil {
		updated, err := addHostToTraefikRule(existing, req.Host)
		if err != nil {
			return nil, err
		}
		if err := writeFileAtomic(jobPath, []byte(updated)); err != nil {
			return nil, err
		}
		if err := s.run(ctx, "nomad", "job", "run", jobPath); err != nil {
			return nil, err
		}
	} else {
		return nil, fmt.Errorf("could not find rendered job file for %s; re-deploy first", req.App)
	}

	resp := &api.DomainAttachResponse{
		App:  req.App,
		Host: req.Host,
		URL:  "https://" + req.Host,
		Mode: mode,
	}
	if mode == "user-external" {
		// Print DNS records the user must create.
		platformIP := s.cfg.PlatformPublicIP
		if platformIP == "" {
			platformIP = "<your-platform-public-ip>"
		}
		resp.DNSRecords = []api.DNSRecord{
			{Type: "A", Name: req.Host, Value: platformIP, TTL: 300},
		}
	}
	return resp, nil
}

// joinPath, readFile, writeFileAtomic — small helpers shared with attachDomain.
func joinPath(parts ...string) string { return filepath.Join(parts...) }

func readFile(p string) (string, error) {
	b, err := os.ReadFile(p)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func writeFileAtomic(p string, b []byte) error {
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// addHostToTraefikRule rewrites a rendered Nomad HCL file to include `host`
// in any Host(...) router rules. Idempotent.
func addHostToTraefikRule(hcl, host string) (string, error) {
	if strings.Contains(hcl, "Host(`"+host+"`)") {
		return hcl, nil
	}
	re := regexp.MustCompile(`(traefik\.http\.routers\.[a-zA-Z0-9-]+\.rule=)([^"]+)`)
	matches := re.FindAllStringIndex(hcl, -1)
	if matches == nil {
		return "", errors.New("could not find Traefik router rule to update")
	}
	out := re.ReplaceAllStringFunc(hcl, func(match string) string {
		parts := re.FindStringSubmatch(match)
		if len(parts) != 3 {
			return match
		}
		return parts[1] + parts[2] + " || Host(`" + host + "`)"
	})
	return out, nil
}
