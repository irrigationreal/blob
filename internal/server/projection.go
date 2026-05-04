package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"

	"github.com/darvell/blob/internal/api"
)

const (
	projectionHashVersion = 1
	projectionMetaKey     = "blob_projection_hash"
)

type jobProjection struct {
	Version    int               `json:"version"`
	ID         string            `json:"id"`
	Datacenter string            `json:"datacenter"`
	Image      string            `json:"image"`
	Port       int               `json:"port,omitempty"`
	Domain     string            `json:"domain,omitempty"`
	App        string            `json:"app"`
	Env        string            `json:"env,omitempty"`
	Form       string            `json:"form"`
	Isolation  string            `json:"isolation,omitempty"`
	Domains    []string          `json:"domains,omitempty"`
	Command    []string          `json:"command,omitempty"`
	CPU        int               `json:"cpu"`
	Memory     int               `json:"memory"`
	Replicas   int               `json:"replicas"`
	LiteralEnv map[string]string `json:"literal_env,omitempty"`
	Services   []string          `json:"services,omitempty"`
	Schedule   string            `json:"schedule,omitempty"`
	Volumes    []api.VolumeMount `json:"volumes,omitempty"`
	Sidecars   []api.Sidecar     `json:"sidecars,omitempty"`
	Root       string            `json:"root,omitempty"`
	Index      string            `json:"index,omitempty"`
	NotFound   string            `json:"not_found,omitempty"`
	SPA        bool              `json:"spa,omitempty"`
}

func projectionHashFromJobFile(hcl string) string {
	needle := projectionMetaKey + " = \""
	i := strings.Index(hcl, needle)
	if i < 0 {
		return ""
	}
	j := i + len(needle)
	k := strings.Index(hcl[j:], "\"")
	if k < 0 {
		return ""
	}
	return hcl[j : j+k]
}

func hashJobProjection(req *api.DeployRequest, image string, port int, domain, dc, id string) string {
	p := jobProjection{
		Version:    projectionHashVersion,
		ID:         id,
		Datacenter: dc,
		Image:      image,
		Port:       port,
		Domain:     domain,
		App:        req.App,
		Env:        req.Environment,
		Form:       req.Form,
		Isolation:  normalizeIsolation(req.Isolation),
		Domains:    req.Domains,
		Command:    req.Command,
		CPU:        req.CPU,
		Memory:     req.Memory,
		Replicas:   req.Replicas,
		LiteralEnv: req.Env,
		Services:   req.Services,
		Schedule:   req.Schedule,
		Volumes:    req.Volumes,
		Sidecars:   req.Sidecars,
		Root:       req.Root,
		Index:      req.Index,
		NotFound:   req.NotFound,
		SPA:        req.SPA,
	}
	if p.Form == "" {
		p.Form = "web-service"
	}
	b, _ := json.Marshal(p)
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}
