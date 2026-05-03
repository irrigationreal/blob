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
	App    string   `json:"app"`
	Lines  []string `json:"lines"`
	Source string   `json:"source,omitempty"` // "loki" or "nomad"
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
	Path      string    `json:"path"`     // server-side path to the .sql.gz file (empty if local file was pruned)
	Filename  string    `json:"filename"` // basename only (UTC-ISO timestamp)
	BytesSize int64     `json:"bytes_size"`
	CreatedAt time.Time `json:"created_at"`
	SHA256    string    `json:"sha256,omitempty"`     // hex of file contents
	Local     bool      `json:"local"`                // present on the platform host disk
	Remote    bool      `json:"remote,omitempty"`     // shipped to off-host destination
	RemoteURL string    `json:"remote_url,omitempty"` // s3://bucket/prefix/filename when shipped
}

type ListPostgresBackupsResponse struct {
	Backups []PostgresBackup `json:"backups"`
}

type RestorePostgresRequest struct {
	Path  string `json:"path,omitempty"`  // explicit backup path or filename; "" or "latest" picks the newest
	From  string `json:"from,omitempty"`  // "local" (default) or "s3"; or a full s3://bucket/key URL
	Force bool   `json:"force,omitempty"` // required when the database is non-empty
}

// Postgres projects (per-project users + databases on a shared instance).
//
// A project is a (role, database, password, statement_timeout_ms) tuple living
// on a Postgres instance. Apps bind to projects via `services: [<instance>.<project>]`
// and receive DATABASE_URL/PG* env vars scoped to that role and database — they
// cannot see other projects' data.
type PostgresProject struct {
	Instance           string    `json:"instance"`
	Project            string    `json:"project"`
	Role               string    `json:"role"`
	Database           string    `json:"database"`
	URLMasked          string    `json:"url"`                  // postgres://<role>:***@host:port/<db>?sslmode=disable
	StatementTimeoutMS int       `json:"statement_timeout_ms"` // applied via ALTER ROLE ... SET statement_timeout
	CreatedAt          time.Time `json:"created_at"`
}

type CreatePostgresProjectRequest struct {
	Project            string `json:"project"`
	StatementTimeoutMS int    `json:"statement_timeout_ms,omitempty"` // default 30000 (30s)
}

type ListPostgresProjectsResponse struct {
	Projects []PostgresProject `json:"projects"`
}

type SetPostgresProjectTimeoutRequest struct {
	StatementTimeoutMS int `json:"statement_timeout_ms"`
}

type PostgresProjectURL struct {
	URL string `json:"url"` // full postgres://<role>:<password>@host:port/<db>
}

// Off-host backup configuration (per Postgres instance).
type PostgresBackupConfig struct {
	Instance         string `json:"instance"`
	DestinationKind  string `json:"destination_kind"`             // currently only "s3" (S3-compatible incl. MinIO/R2/B2)
	S3Endpoint       string `json:"s3_endpoint,omitempty"`        // e.g. https://minio.irrigate.cc; empty = AWS public endpoints
	S3Region         string `json:"s3_region,omitempty"`          // e.g. us-east-1; default us-east-1
	S3Bucket         string `json:"s3_bucket"`
	S3Prefix         string `json:"s3_prefix,omitempty"`          // e.g. demo/ ; trailing slash optional
	S3AccessKeyID    string `json:"s3_access_key_id"`
	S3SecretAccessKey string `json:"s3_secret_access_key,omitempty"` // never emitted in GET responses; mask via API
	S3UsePathStyle   bool   `json:"s3_use_path_style,omitempty"`  // MinIO/R2 default true; AWS false
	Schedule         string `json:"schedule,omitempty"`           // 5-field cron in UTC; default "0 3 * * *"
	RetentionDaily   int    `json:"retention_daily,omitempty"`    // default 7
	RetentionWeekly  int    `json:"retention_weekly,omitempty"`   // default 4
	RetentionMonthly int    `json:"retention_monthly,omitempty"`  // default 6
	Enabled          bool   `json:"enabled"`
}

type SetPostgresBackupConfigRequest struct {
	Config PostgresBackupConfig `json:"config"`
}

type TestPostgresBackupConfigResponse struct {
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
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

// Managed services — Loki (v0.8 observability)
type Loki struct {
	Name      string    `json:"name"`
	Version   string    `json:"version"`
	Host      string    `json:"host"`
	Port      int       `json:"port"`
	JobID     string    `json:"job_id"`
	URL       string    `json:"url"` // http://host:port (no auth — bind to private net)
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
}

type CreateLokiRequest struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"` // default 3.2
	CPU     int    `json:"cpu,omitempty"`
	Memory  int    `json:"memory,omitempty"`
}

type ListLokiResponse struct {
	Loki []Loki `json:"loki"`
}

// Managed services — Grafana (v0.8 observability)
type Grafana struct {
	Name      string    `json:"name"`
	Version   string    `json:"version"`
	Host      string    `json:"host"`
	Port      int       `json:"port"`
	JobID     string    `json:"job_id"`
	URL       string    `json:"url"`
	LokiURL   string    `json:"loki_url,omitempty"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
}

type CreateGrafanaRequest struct {
	Name               string `json:"name"`
	Version            string `json:"version,omitempty"` // default 11
	CPU                int    `json:"cpu,omitempty"`
	Memory             int    `json:"memory,omitempty"`
	LokiInstance       string `json:"loki_instance,omitempty"`       // optional managed-loki name
	TempoInstance      string `json:"tempo_instance,omitempty"`      // optional managed-tempo name
	PrometheusInstance string `json:"prometheus_instance,omitempty"` // optional managed-prometheus name
}

type ListGrafanaResponse struct {
	Grafana []Grafana `json:"grafana"`
}

type GrafanaURL struct {
	URL           string `json:"url"`
	AdminPassword string `json:"admin_password"`
}

// Managed services — Promtail (v0.8 observability)
type Promtail struct {
	Name         string    `json:"name"`
	Version      string    `json:"version"`
	JobID        string    `json:"job_id"`
	LokiInstance string    `json:"loki_instance"`
	LokiURL      string    `json:"loki_url"`
	Status       string    `json:"status"`
	CreatedAt    time.Time `json:"created_at"`
}

type CreatePromtailRequest struct {
	Name         string `json:"name"`
	Version      string `json:"version,omitempty"` // default 3.2
	CPU          int    `json:"cpu,omitempty"`
	Memory       int    `json:"memory,omitempty"`
	LokiInstance string `json:"loki_instance"` // required — which managed Loki to ship to
}

type ListPromtailResponse struct {
	Promtail []Promtail `json:"promtail"`
}

// Managed services — NATS (v0.10 messaging)
type NATS struct {
	Name      string    `json:"name"`
	Version   string    `json:"version"`
	Host      string    `json:"host"`
	Port      int       `json:"port"`
	JobID     string    `json:"job_id"`
	URL       string    `json:"url"` // nats://host:port
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
}

type CreateNATSRequest struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
	CPU     int    `json:"cpu,omitempty"`
	Memory  int    `json:"memory,omitempty"`
}

type ListNATSResponse struct {
	NATS []NATS `json:"nats"`
}

// Managed services — Tempo (v0.10 distributed tracing)
type Tempo struct {
	Name      string    `json:"name"`
	Version   string    `json:"version"`
	Host      string    `json:"host"`
	HTTPPort  int       `json:"http_port"`
	OTLPPort  int       `json:"otlp_port"`
	JobID     string    `json:"job_id"`
	URL       string    `json:"url"`       // http://host:http_port
	OTLPGRPC  string    `json:"otlp_grpc"` // host:otlp_port
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
}

type CreateTempoRequest struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
	CPU     int    `json:"cpu,omitempty"`
	Memory  int    `json:"memory,omitempty"`
}

type ListTempoResponse struct {
	Tempo []Tempo `json:"tempo"`
}

// Managed services — Prometheus (v0.10 metrics)
type Prometheus struct {
	Name      string    `json:"name"`
	Version   string    `json:"version"`
	Host      string    `json:"host"`
	Port      int       `json:"port"`
	JobID     string    `json:"job_id"`
	URL       string    `json:"url"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
}

type CreatePrometheusRequest struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
	CPU     int    `json:"cpu,omitempty"`
	Memory  int    `json:"memory,omitempty"`
}

type ListPrometheusResponse struct {
	Prometheus []Prometheus `json:"prometheus"`
}

// Autoscale (v0.11) — per-app horizontal autoscaler config.
type AutoscaleConfig struct {
	App          string        `json:"app"`
	Enabled      bool          `json:"enabled"`
	Min          int           `json:"min"`
	Max          int           `json:"max"`
	Metric       string        `json:"metric"`        // "cpu" | "memory" | "http_qps" | raw PromQL
	Target       float64       `json:"target"`        // metric value at which we want to be at the current scale
	CooldownUp   time.Duration `json:"cooldown_up"`   // min interval between scale-ups (Go duration)
	CooldownDown time.Duration `json:"cooldown_down"` // min interval between scale-downs
}

type ListAutoscaleResponse struct {
	Autoscale []AutoscaleConfig `json:"autoscale"`
}

// Services rollup (v0.11) — single endpoint that fans out to every
// managed-service registry.
type ServiceSummary struct {
	Kind   string   `json:"kind"`   // postgres | valkey | loki | grafana | promtail | nats | tempo | prometheus
	Name   string   `json:"name"`
	Status string   `json:"status"`
	Host   string   `json:"host,omitempty"`
	Ports  []int    `json:"ports,omitempty"`
	URLs   []string `json:"urls,omitempty"`
}

type ListServicesResponse struct {
	Services []ServiceSummary `json:"services"`
}
