package api

import "time"

// DeployRequest covers single-component deploys (web-service, daemon, job, cronjob).
// Multi-component apps are sent through DeployAppRequest.
type DeployRequest struct {
	App         string            `json:"app"`
	Environment string            `json:"environment,omitempty"`
	Domain      string            `json:"domain,omitempty"`
	Port        int               `json:"port,omitempty"`
	Tag         string            `json:"tag,omitempty"`
	Command     []string          `json:"command,omitempty"`
	CPU         int               `json:"cpu,omitempty"`
	Memory      int               `json:"memory,omitempty"`
	Replicas    int               `json:"replicas,omitempty"`
	Env         map[string]string `json:"env,omitempty"`
	Secrets     []SecretBinding   `json:"secrets,omitempty"`
	Form        string            `json:"form,omitempty"`     // web-service | daemon | job | cronjob
	Schedule    string            `json:"schedule,omitempty"` // cron expression for cronjob
	Volumes     []VolumeMount     `json:"volumes,omitempty"`
	Sidecars    []Sidecar         `json:"sidecars,omitempty"`
}

type SecretBinding struct {
	Env  string `json:"env"`
	Name string `json:"name"`
}

type VolumeMount struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

type Sidecar struct {
	Name   string            `json:"name"`
	Image  string            `json:"image"`
	CPU    int               `json:"cpu,omitempty"`
	Memory int               `json:"memory,omitempty"`
	Env    map[string]string `json:"env,omitempty"`
	Args   []string          `json:"args,omitempty"`
}

// DeployAppRequest deploys a multi-component App. Each Component becomes its
// own Nomad job; the App as a whole shares the env/environment.
type DeployAppRequest struct {
	App         string             `json:"app"`
	Environment string             `json:"environment,omitempty"`
	Components  []DeployRequest    `json:"components"`
}

type DeployResponse struct {
	App         string    `json:"app"`
	Environment string    `json:"environment,omitempty"`
	Domain      string    `json:"domain"`
	Image       string    `json:"image"`
	URL         string    `json:"url"`
	JobID       string    `json:"job_id"`
	StartedAt   time.Time `json:"started_at"`
	Phases      []Phase   `json:"phases"`
}

type DeployAppResponse struct {
	App        string           `json:"app"`
	Components []DeployResponse `json:"components"`
}

type Phase struct {
	Name       string    `json:"name"`
	DurationMS int64     `json:"duration_ms"`
	OK         bool      `json:"ok"`
	Note       string    `json:"note,omitempty"`
	When       time.Time `json:"when"`
}

type AppSummary struct {
	App         string    `json:"app"`
	Environment string    `json:"environment,omitempty"`
	Domain      string    `json:"domain"`
	Image       string    `json:"image"`
	URL         string    `json:"url"`
	Status      string    `json:"status"`
	Form        string    `json:"form"`
	Replicas    int       `json:"replicas"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type ListResponse struct {
	Apps []AppSummary `json:"apps"`
}

type LogsResponse struct {
	App   string   `json:"app"`
	Lines []string `json:"lines"`
}

type StatusResponse struct {
	AppSummary
	Allocations []Allocation `json:"allocations"`
}

type Allocation struct {
	ID     string `json:"id"`
	Node   string `json:"node"`
	Status string `json:"status"`
	Health string `json:"health"`
}

type ErrorBody struct {
	Error string `json:"error"`
}

type WhoAmI struct {
	Name string `json:"name"`
	OK   bool   `json:"ok"`
}

// Secrets API
type Secret struct {
	Name        string    `json:"name"`
	Environment string    `json:"environment,omitempty"`
	UpdatedAt   time.Time `json:"updated_at"`
	Length      int       `json:"length,omitempty"` // value length, never the value
}

type SetSecretRequest struct {
	Name        string `json:"name"`
	Environment string `json:"environment,omitempty"`
	Value       string `json:"value"`
}

type ListSecretsResponse struct {
	Secrets []Secret `json:"secrets"`
}

// Scale API
type ScaleRequest struct {
	Replicas int `json:"replicas"`
}

// Domains API
type Domain struct {
	Host string `json:"host"`
	App  string `json:"app"`
	Mode string `json:"mode"` // platform-base | user-managed | user-external
}

type AttachDomainRequest struct {
	App  string `json:"app"`
	Host string `json:"host"`
}

// Doctor
type DoctorIssue struct {
	Severity   string `json:"severity"`   // P1 | P2 | P3 | info
	Category   string `json:"category"`   // routing | nomad | registry | secrets | drift | host
	App        string `json:"app,omitempty"`
	Title      string `json:"title"`
	Detail     string `json:"detail,omitempty"`
	Remediate  string `json:"remediate,omitempty"`
}

type DoctorResponse struct {
	Issues  []DoctorIssue `json:"issues"`
	Checked int           `json:"checked"`
}

// Volumes
type Volume struct {
	Name      string `json:"name"`
	App       string `json:"app"`
	HostName  string `json:"host_name"` // Docker volume name on the host
	BytesUsed int64  `json:"bytes_used,omitempty"`
}

type ListVolumesResponse struct {
	Volumes []Volume `json:"volumes"`
}
