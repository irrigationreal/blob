package api

import "time"

// DeployRequest covers single-component deploys (web-service, daemon, job, cronjob).
// Multi-component apps are sent through DeployAppRequest.
type DeployRequest struct {
	App         string            `json:"app"`
	Environment string            `json:"environment,omitempty"`
	Domain      string            `json:"domain,omitempty"`
	Domains     []string          `json:"domains,omitempty"`
	Port        int               `json:"port,omitempty"`
	Tag         string            `json:"tag,omitempty"`
	Command     []string          `json:"command,omitempty"`
	CPU         int               `json:"cpu,omitempty"`
	Memory      int               `json:"memory,omitempty"`
	Replicas    int               `json:"replicas,omitempty"`
	Env         map[string]string `json:"env,omitempty"`
	Secrets     []SecretBinding   `json:"secrets,omitempty"`
	Services    []string          `json:"services,omitempty"` // names of managed services to bind (e.g. ["my-pg"])
	Form        string            `json:"form,omitempty"`     // web-service | daemon | job | cronjob | static
	Schedule    string            `json:"schedule,omitempty"` // cron expression for cronjob
	Volumes     []VolumeMount     `json:"volumes,omitempty"`
	Sidecars    []Sidecar         `json:"sidecars,omitempty"`

	// Static-site fields (form: static)
	Root     string `json:"root,omitempty"`
	Build    string `json:"build,omitempty"`
	Index    string `json:"index,omitempty"`
	NotFound string `json:"not_found,omitempty"`
	SPA      bool   `json:"spa,omitempty"`
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

// Nodes
type Node struct {
	ID         string            `json:"id"`
	Name       string            `json:"name"`
	Address    string            `json:"address"`
	Datacenter string            `json:"datacenter"`
	Status     string            `json:"status"`     // ready | down | initializing
	Eligible   string            `json:"eligible"`   // eligible | ineligible
	Drain      bool              `json:"drain"`
	NodeClass  string            `json:"node_class,omitempty"`
	Labels     map[string]string `json:"labels,omitempty"`
}

type ListNodesResponse struct {
	Nodes []Node `json:"nodes"`
}

type JoinTokenResponse struct {
	Address    string `json:"address"`     // host:port of Nomad server
	Token      string `json:"token"`       // bootstrap token (or empty if ACLs disabled)
	JoinScript string `json:"join_script"` // sh one-liner for the new node
}

// Custom domain attach. Mode is one of: platform-base, user-managed, user-external.
type DomainAttachRequest struct {
	App   string `json:"app"`
	Host  string `json:"host"`
	Mode  string `json:"mode,omitempty"` // optional; default platform-base/user-external auto-detected
}

type DomainAttachResponse struct {
	App         string             `json:"app"`
	Host        string             `json:"host"`
	URL         string             `json:"url"`
	Mode        string             `json:"mode"`
	DNSRecords  []DNSRecord        `json:"dns_records,omitempty"` // for user-external mode
}

type DNSRecord struct {
	Type  string `json:"type"`  // A | CNAME | TXT
	Name  string `json:"name"`
	Value string `json:"value"`
	TTL   int    `json:"ttl,omitempty"`
}

// Releases
type Release struct {
	Revision  int       `json:"revision"`
	JobID     string    `json:"job_id"`
	Image     string    `json:"image"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
}

type ListReleasesResponse struct {
	Releases []Release `json:"releases"`
}

// Exec
type ExecRequest struct {
	Command []string `json:"command"`
}

type ExecResponse struct {
	Output   string `json:"output"`
	ExitCode int    `json:"exit_code"`
}

// Managed services — Postgres (more drivers later)
type Postgres struct {
	Name      string    `json:"name"`
	Version   string    `json:"version"`
	Database  string    `json:"database"`
	User      string    `json:"user"`
	Host      string    `json:"host"`     // public host other workloads connect through
	Port      int       `json:"port"`     // host static port allocated for this instance
	JobID     string    `json:"job_id"`
	URLMasked string    `json:"url"`      // postgres://user:***@host:port/db
	Status    string    `json:"status"`   // running | pending | dead
	CreatedAt time.Time `json:"created_at"`
}

type CreatePostgresRequest struct {
	Name     string `json:"name"`
	Version  string `json:"version,omitempty"`  // default 16
	Database string `json:"database,omitempty"` // default = name
	CPU      int    `json:"cpu,omitempty"`
	Memory   int    `json:"memory,omitempty"`
	Disk     int    `json:"disk,omitempty"` // MiB
}

type ListPostgresResponse struct {
	Postgres []Postgres `json:"postgres"`
}

type PostgresURL struct {
	URL string `json:"url"` // full postgres://... including password
}

// Postgres backups
type PostgresBackup struct {
	Name      string    `json:"name"`     // postgres instance name
	Path      string    `json:"path"`     // server-side path to the .sql.gz file
	Filename  string    `json:"filename"` // basename only (UTC-ISO timestamp)
	BytesSize int64     `json:"bytes_size"`
	CreatedAt time.Time `json:"created_at"`
}

type ListPostgresBackupsResponse struct {
	Backups []PostgresBackup `json:"backups"`
}

type RestorePostgresRequest struct {
	Path  string `json:"path,omitempty"`  // explicit backup path or filename; "" or "latest" picks the newest
	Force bool   `json:"force,omitempty"` // required when the database is non-empty
}

// Managed services — Valkey
type Valkey struct {
	Name      string    `json:"name"`
	Version   string    `json:"version"`
	Host      string    `json:"host"`
	Port      int       `json:"port"`
	JobID     string    `json:"job_id"`
	URLMasked string    `json:"url"`    // redis://:***@host:port
	Status    string    `json:"status"` // running | pending | dead
	CreatedAt time.Time `json:"created_at"`
}

type CreateValkeyRequest struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"` // default 8
	CPU     int    `json:"cpu,omitempty"`
	Memory  int    `json:"memory,omitempty"`
}

type ListValkeyResponse struct {
	Valkey []Valkey `json:"valkey"`
}

type ValkeyURL struct {
	URL string `json:"url"` // full redis://:<password>@host:port
}
