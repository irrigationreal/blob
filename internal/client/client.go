// Package client is a thin HTTP client for the blobd API.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/darvell/blob/internal/api"
)

type Client struct {
	BaseURL    string
	Token      string
	HTTPClient *http.Client
}

func New(base, token string) *Client {
	return &Client{
		BaseURL:    strings.TrimRight(base, "/"),
		Token:      token,
		HTTPClient: &http.Client{Timeout: 30 * time.Minute},
	}
}

func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, rd)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("call %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		var e api.ErrorBody
		_ = json.Unmarshal(respBody, &e)
		if e.Error != "" {
			return fmt.Errorf("%s %s: %s", method, path, e.Error)
		}
		return fmt.Errorf("%s %s: %s: %s", method, path, resp.Status, string(respBody))
	}
	if out != nil {
		if err := json.Unmarshal(respBody, out); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
	}
	return nil
}

func (c *Client) WhoAmI(ctx context.Context) (*api.WhoAmI, error) {
	out := &api.WhoAmI{}
	if err := c.do(ctx, "GET", "/v1/whoami", nil, out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) UploadSource(ctx context.Context, app string, tarStream io.Reader) error {
	req, err := http.NewRequestWithContext(ctx, "POST", c.BaseURL+"/v1/sources/"+url.PathEscape(app), tarStream)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-tar+gzip")
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("upload source: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("upload source: %s: %s", resp.Status, string(body))
	}
	return nil
}

func (c *Client) Deploy(ctx context.Context, req *api.DeployRequest) (*api.DeployResponse, error) {
	out := &api.DeployResponse{}
	if err := c.do(ctx, "POST", "/v1/deploy", req, out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) DeployImage(ctx context.Context, req *api.DeployRequest) (*api.DeployResponse, error) {
	out := &api.DeployResponse{}
	if err := c.do(ctx, "POST", "/v1/deploy-image", req, out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) List(ctx context.Context) (*api.ListResponse, error) {
	out := &api.ListResponse{}
	if err := c.do(ctx, "GET", "/v1/apps", nil, out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) Status(ctx context.Context, app string) (*api.StatusResponse, error) {
	out := &api.StatusResponse{}
	if err := c.do(ctx, "GET", "/v1/apps/"+url.PathEscape(app), nil, out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) Logs(ctx context.Context, app string, lines int) (*api.LogsResponse, error) {
	return c.LogsWithOptions(ctx, app, lines, "", "")
}

// LogsWithOptions queries server-side logs. since (e.g. "5m") and grep
// trigger the Loki path when a Loki instance is registered; without them
// the server falls back to nomad alloc tail.
func (c *Client) LogsWithOptions(ctx context.Context, app string, lines int, since, grep string) (*api.LogsResponse, error) {
	q := url.Values{}
	q.Set("lines", fmt.Sprintf("%d", lines))
	if since != "" {
		q.Set("since", since)
	}
	if grep != "" {
		q.Set("grep", grep)
	}
	out := &api.LogsResponse{}
	if err := c.do(ctx, "GET", fmt.Sprintf("/v1/apps/%s/logs?%s", url.PathEscape(app), q.Encode()), nil, out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) Destroy(ctx context.Context, app string) error {
	return c.do(ctx, "DELETE", "/v1/apps/"+url.PathEscape(app), nil, nil)
}

func (c *Client) Scale(ctx context.Context, app string, replicas int) error {
	return c.do(ctx, "POST", "/v1/apps/"+url.PathEscape(app)+"/scale", &api.ScaleRequest{Replicas: replicas}, nil)
}

func (c *Client) DeployApp(ctx context.Context, req *api.DeployAppRequest) (*api.DeployAppResponse, error) {
	out := &api.DeployAppResponse{}
	if err := c.do(ctx, "POST", "/v1/deploy-app", req, out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) ListSecrets(ctx context.Context, env string) (*api.ListSecretsResponse, error) {
	out := &api.ListSecretsResponse{}
	q := ""
	if env != "" {
		q = "?environment=" + url.QueryEscape(env)
	}
	if err := c.do(ctx, "GET", "/v1/secrets"+q, nil, out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) SetSecret(ctx context.Context, env, name, value string) error {
	return c.do(ctx, "POST", "/v1/secrets", &api.SetSecretRequest{Name: name, Environment: env, Value: value}, nil)
}

func (c *Client) DeleteSecret(ctx context.Context, env, name string) error {
	q := ""
	if env != "" {
		q = "?environment=" + url.QueryEscape(env)
	}
	return c.do(ctx, "DELETE", "/v1/secrets/"+url.PathEscape(name)+q, nil, nil)
}

func (c *Client) Doctor(ctx context.Context) (*api.DoctorResponse, error) {
	out := &api.DoctorResponse{}
	if err := c.do(ctx, "GET", "/v1/doctor", nil, out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) ListStatusPages(ctx context.Context) (*api.ListStatusPagesResponse, error) {
	out := &api.ListStatusPagesResponse{}
	if err := c.do(ctx, "GET", "/v1/status-pages", nil, out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) EnableStatusPage(ctx context.Context, app string) (*api.StatusPageResponse, error) {
	out := &api.StatusPageResponse{}
	req := &api.EnableStatusPageRequest{App: app}
	if err := c.do(ctx, "POST", "/v1/status-pages", req, out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) ShowStatusPage(ctx context.Context, app string) (*api.StatusPageResponse, error) {
	out := &api.StatusPageResponse{}
	if err := c.do(ctx, "GET", "/v1/status-pages/"+url.PathEscape(app), nil, out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) DisableStatusPage(ctx context.Context, app string) error {
	return c.do(ctx, "DELETE", "/v1/status-pages/"+url.PathEscape(app), nil, nil)
}

func (c *Client) ListAudit(ctx context.Context, limit int) (*api.ListAuditResponse, error) {
	q := ""
	if limit > 0 {
		q = "?limit=" + fmt.Sprintf("%d", limit)
	}
	out := &api.ListAuditResponse{}
	if err := c.do(ctx, "GET", "/v1/audit"+q, nil, out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) GetAudit(ctx context.Context, id string) (*api.AuditEvent, error) {
	out := &api.AuditEvent{}
	if err := c.do(ctx, "GET", "/v1/audit/"+url.PathEscape(id), nil, out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) ListNodes(ctx context.Context) (*api.ListNodesResponse, error) {
	out := &api.ListNodesResponse{}
	if err := c.do(ctx, "GET", "/v1/nodes", nil, out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) RecommendPlacement(ctx context.Context, cpu, memory, disk int) (*api.PlacementRecommendation, error) {
	q := url.Values{}
	q.Set("cpu", fmt.Sprint(cpu))
	q.Set("memory", fmt.Sprint(memory))
	if disk > 0 {
		q.Set("disk", fmt.Sprint(disk))
	}
	out := &api.PlacementRecommendation{}
	if err := c.do(ctx, "GET", "/v1/nodes/recommend?"+q.Encode(), nil, out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) DrainNode(ctx context.Context, id string, on bool) error {
	if on {
		return c.do(ctx, "POST", "/v1/nodes/"+url.PathEscape(id)+"/drain", nil, nil)
	}
	return c.do(ctx, "DELETE", "/v1/nodes/"+url.PathEscape(id)+"/drain", nil, nil)
}

func (c *Client) JoinScript(ctx context.Context) (*api.JoinTokenResponse, error) {
	out := &api.JoinTokenResponse{}
	if err := c.do(ctx, "GET", "/v1/join", nil, out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) ListVolumes(ctx context.Context) (*api.ListVolumesResponse, error) {
	out := &api.ListVolumesResponse{}
	if err := c.do(ctx, "GET", "/v1/volumes", nil, out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) Restart(ctx context.Context, app string) error {
	return c.do(ctx, "POST", "/v1/apps/"+url.PathEscape(app)+"/restart", nil, nil)
}

func (c *Client) Releases(ctx context.Context, app string) (*api.ListReleasesResponse, error) {
	out := &api.ListReleasesResponse{}
	if err := c.do(ctx, "GET", "/v1/apps/"+url.PathEscape(app)+"/releases", nil, out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) AttachDomain(ctx context.Context, app, host, mode string) (*api.DomainAttachResponse, error) {
	out := &api.DomainAttachResponse{}
	if err := c.do(ctx, "POST", "/v1/apps/"+url.PathEscape(app)+"/domains", &api.DomainAttachRequest{Host: host, Mode: mode}, out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) Exec(ctx context.Context, app string, cmd []string) (*api.ExecResponse, error) {
	out := &api.ExecResponse{}
	if err := c.do(ctx, "POST", "/v1/apps/"+url.PathEscape(app)+"/exec", &api.ExecRequest{Command: cmd}, out); err != nil {
		return nil, err
	}
	return out, nil
}

// --- managed services: postgres ---

func (c *Client) ListPostgres(ctx context.Context) (*api.ListPostgresResponse, error) {
	out := &api.ListPostgresResponse{}
	if err := c.do(ctx, "GET", "/v1/postgres", nil, out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) CreatePostgres(ctx context.Context, req *api.CreatePostgresRequest) (*api.Postgres, error) {
	out := &api.Postgres{}
	if err := c.do(ctx, "POST", "/v1/postgres", req, out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) GetPostgres(ctx context.Context, name string) (*api.Postgres, error) {
	out := &api.Postgres{}
	if err := c.do(ctx, "GET", "/v1/postgres/"+url.PathEscape(name), nil, out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) PostgresURL(ctx context.Context, name string) (string, error) {
	out := &api.PostgresURL{}
	if err := c.do(ctx, "GET", "/v1/postgres/"+url.PathEscape(name)+"/url", nil, out); err != nil {
		return "", err
	}
	return out.URL, nil
}

func (c *Client) DestroyPostgres(ctx context.Context, name string) error {
	return c.do(ctx, "DELETE", "/v1/postgres/"+url.PathEscape(name), nil, nil)
}

func (c *Client) BackupPostgres(ctx context.Context, name string) (*api.PostgresBackup, error) {
	out := &api.PostgresBackup{}
	if err := c.do(ctx, "POST", "/v1/postgres/"+url.PathEscape(name)+"/backup", nil, out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) ListPostgresBackups(ctx context.Context, name string) (*api.ListPostgresBackupsResponse, error) {
	out := &api.ListPostgresBackupsResponse{}
	if err := c.do(ctx, "GET", "/v1/postgres/"+url.PathEscape(name)+"/backups", nil, out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) RestorePostgres(ctx context.Context, name, path string, force bool) error {
	return c.do(ctx, "POST", "/v1/postgres/"+url.PathEscape(name)+"/restore", &api.RestorePostgresRequest{Path: path, Force: force}, nil)
}

func (c *Client) RestorePostgresFrom(ctx context.Context, name, path, from string, force bool) error {
	return c.do(ctx, "POST", "/v1/postgres/"+url.PathEscape(name)+"/restore", &api.RestorePostgresRequest{Path: path, From: from, Force: force}, nil)
}

// --- managed services: postgres backup-config (off-host shipping) ---

func (c *Client) GetPostgresBackupConfig(ctx context.Context, instance string) (*api.PostgresBackupConfig, error) {
	out := &api.PostgresBackupConfig{}
	if err := c.do(ctx, "GET", "/v1/postgres/"+url.PathEscape(instance)+"/backup-config", nil, out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) SetPostgresBackupConfig(ctx context.Context, cfg *api.PostgresBackupConfig) (*api.PostgresBackupConfig, error) {
	out := &api.PostgresBackupConfig{}
	req := &api.SetPostgresBackupConfigRequest{Config: *cfg}
	if err := c.do(ctx, "PUT", "/v1/postgres/"+url.PathEscape(cfg.Instance)+"/backup-config", req, out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) ClearPostgresBackupConfig(ctx context.Context, instance string) error {
	return c.do(ctx, "DELETE", "/v1/postgres/"+url.PathEscape(instance)+"/backup-config", nil, nil)
}

func (c *Client) TestPostgresBackupConfig(ctx context.Context, instance string) (*api.TestPostgresBackupConfigResponse, error) {
	out := &api.TestPostgresBackupConfigResponse{}
	if err := c.do(ctx, "POST", "/v1/postgres/"+url.PathEscape(instance)+"/backup-config/test", nil, out); err != nil {
		return nil, err
	}
	return out, nil
}

// --- managed services: postgres projects (per-tenant role + database) ---

func (c *Client) ListPostgresProjects(ctx context.Context, instance string) (*api.ListPostgresProjectsResponse, error) {
	out := &api.ListPostgresProjectsResponse{}
	if err := c.do(ctx, "GET", "/v1/postgres/"+url.PathEscape(instance)+"/projects", nil, out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) CreatePostgresProject(ctx context.Context, instance, project string, timeoutMS int) (*api.PostgresProject, error) {
	out := &api.PostgresProject{}
	req := &api.CreatePostgresProjectRequest{Project: project, StatementTimeoutMS: timeoutMS}
	if err := c.do(ctx, "POST", "/v1/postgres/"+url.PathEscape(instance)+"/projects", req, out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) PostgresProjectURL(ctx context.Context, instance, project string) (string, error) {
	out := &api.PostgresProjectURL{}
	if err := c.do(ctx, "GET", "/v1/postgres/"+url.PathEscape(instance)+"/projects/"+url.PathEscape(project)+"/url", nil, out); err != nil {
		return "", err
	}
	return out.URL, nil
}

func (c *Client) DestroyPostgresProject(ctx context.Context, instance, project string) error {
	return c.do(ctx, "DELETE", "/v1/postgres/"+url.PathEscape(instance)+"/projects/"+url.PathEscape(project), nil, nil)
}

func (c *Client) SetPostgresProjectTimeout(ctx context.Context, instance, project string, timeoutMS int) (*api.PostgresProject, error) {
	out := &api.PostgresProject{}
	req := &api.SetPostgresProjectTimeoutRequest{StatementTimeoutMS: timeoutMS}
	if err := c.do(ctx, "POST", "/v1/postgres/"+url.PathEscape(instance)+"/projects/"+url.PathEscape(project)+"/timeout", req, out); err != nil {
		return nil, err
	}
	return out, nil
}

// --- managed services: valkey ---

func (c *Client) ListValkey(ctx context.Context) (*api.ListValkeyResponse, error) {
	out := &api.ListValkeyResponse{}
	if err := c.do(ctx, "GET", "/v1/valkey", nil, out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) CreateValkey(ctx context.Context, req *api.CreateValkeyRequest) (*api.Valkey, error) {
	out := &api.Valkey{}
	if err := c.do(ctx, "POST", "/v1/valkey", req, out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) ValkeyURL(ctx context.Context, name string) (string, error) {
	out := &api.ValkeyURL{}
	if err := c.do(ctx, "GET", "/v1/valkey/"+url.PathEscape(name)+"/url", nil, out); err != nil {
		return "", err
	}
	return out.URL, nil
}

func (c *Client) DestroyValkey(ctx context.Context, name string) error {
	return c.do(ctx, "DELETE", "/v1/valkey/"+url.PathEscape(name), nil, nil)
}

// --- Loki ---

func (c *Client) ListLoki(ctx context.Context) (*api.ListLokiResponse, error) {
	out := &api.ListLokiResponse{}
	if err := c.do(ctx, "GET", "/v1/loki", nil, out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) CreateLoki(ctx context.Context, req *api.CreateLokiRequest) (*api.Loki, error) {
	out := &api.Loki{}
	if err := c.do(ctx, "POST", "/v1/loki", req, out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) GetLoki(ctx context.Context, name string) (*api.Loki, error) {
	out := &api.Loki{}
	if err := c.do(ctx, "GET", "/v1/loki/"+url.PathEscape(name), nil, out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) DestroyLoki(ctx context.Context, name string) error {
	return c.do(ctx, "DELETE", "/v1/loki/"+url.PathEscape(name), nil, nil)
}

// --- Grafana ---

func (c *Client) ListGrafana(ctx context.Context) (*api.ListGrafanaResponse, error) {
	out := &api.ListGrafanaResponse{}
	if err := c.do(ctx, "GET", "/v1/grafana", nil, out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) CreateGrafana(ctx context.Context, req *api.CreateGrafanaRequest) (*api.Grafana, error) {
	out := &api.Grafana{}
	if err := c.do(ctx, "POST", "/v1/grafana", req, out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) GrafanaURL(ctx context.Context, name string) (*api.GrafanaURL, error) {
	out := &api.GrafanaURL{}
	if err := c.do(ctx, "GET", "/v1/grafana/"+url.PathEscape(name)+"/url", nil, out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) DestroyGrafana(ctx context.Context, name string) error {
	return c.do(ctx, "DELETE", "/v1/grafana/"+url.PathEscape(name), nil, nil)
}

// --- Promtail ---

func (c *Client) ListPromtail(ctx context.Context) (*api.ListPromtailResponse, error) {
	out := &api.ListPromtailResponse{}
	if err := c.do(ctx, "GET", "/v1/promtail", nil, out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) CreatePromtail(ctx context.Context, req *api.CreatePromtailRequest) (*api.Promtail, error) {
	out := &api.Promtail{}
	if err := c.do(ctx, "POST", "/v1/promtail", req, out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) DestroyPromtail(ctx context.Context, name string) error {
	return c.do(ctx, "DELETE", "/v1/promtail/"+url.PathEscape(name), nil, nil)
}

// --- NATS ---

func (c *Client) ListNATS(ctx context.Context) (*api.ListNATSResponse, error) {
	out := &api.ListNATSResponse{}
	if err := c.do(ctx, "GET", "/v1/nats", nil, out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) CreateNATS(ctx context.Context, req *api.CreateNATSRequest) (*api.NATS, error) {
	out := &api.NATS{}
	if err := c.do(ctx, "POST", "/v1/nats", req, out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) GetNATS(ctx context.Context, name string) (*api.NATS, error) {
	out := &api.NATS{}
	if err := c.do(ctx, "GET", "/v1/nats/"+url.PathEscape(name), nil, out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) DestroyNATS(ctx context.Context, name string) error {
	return c.do(ctx, "DELETE", "/v1/nats/"+url.PathEscape(name), nil, nil)
}

// --- Tempo ---

func (c *Client) ListTempo(ctx context.Context) (*api.ListTempoResponse, error) {
	out := &api.ListTempoResponse{}
	if err := c.do(ctx, "GET", "/v1/tempo", nil, out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) CreateTempo(ctx context.Context, req *api.CreateTempoRequest) (*api.Tempo, error) {
	out := &api.Tempo{}
	if err := c.do(ctx, "POST", "/v1/tempo", req, out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) GetTempo(ctx context.Context, name string) (*api.Tempo, error) {
	out := &api.Tempo{}
	if err := c.do(ctx, "GET", "/v1/tempo/"+url.PathEscape(name), nil, out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) DestroyTempo(ctx context.Context, name string) error {
	return c.do(ctx, "DELETE", "/v1/tempo/"+url.PathEscape(name), nil, nil)
}

// --- Prometheus ---

func (c *Client) ListPrometheus(ctx context.Context) (*api.ListPrometheusResponse, error) {
	out := &api.ListPrometheusResponse{}
	if err := c.do(ctx, "GET", "/v1/prometheus", nil, out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) CreatePrometheus(ctx context.Context, req *api.CreatePrometheusRequest) (*api.Prometheus, error) {
	out := &api.Prometheus{}
	if err := c.do(ctx, "POST", "/v1/prometheus", req, out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) GetPrometheus(ctx context.Context, name string) (*api.Prometheus, error) {
	out := &api.Prometheus{}
	if err := c.do(ctx, "GET", "/v1/prometheus/"+url.PathEscape(name), nil, out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) DestroyPrometheus(ctx context.Context, name string) error {
	return c.do(ctx, "DELETE", "/v1/prometheus/"+url.PathEscape(name), nil, nil)
}

// --- Autoscale (v0.11) ---

func (c *Client) ListAutoscale(ctx context.Context) (*api.ListAutoscaleResponse, error) {
	out := &api.ListAutoscaleResponse{}
	if err := c.do(ctx, "GET", "/v1/autoscale", nil, out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) GetAutoscale(ctx context.Context, app string) (*api.AutoscaleConfig, error) {
	out := &api.AutoscaleConfig{}
	if err := c.do(ctx, "GET", "/v1/autoscale/"+url.PathEscape(app), nil, out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) SetAutoscale(ctx context.Context, app string, cfg *api.AutoscaleConfig) (*api.AutoscaleConfig, error) {
	out := &api.AutoscaleConfig{}
	if err := c.do(ctx, "PUT", "/v1/autoscale/"+url.PathEscape(app), cfg, out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) UnsetAutoscale(ctx context.Context, app string) error {
	return c.do(ctx, "DELETE", "/v1/autoscale/"+url.PathEscape(app), nil, nil)
}

// --- Services rollup (v0.11) ---

func (c *Client) ListServices(ctx context.Context) (*api.ListServicesResponse, error) {
	out := &api.ListServicesResponse{}
	if err := c.do(ctx, "GET", "/v1/services", nil, out); err != nil {
		return nil, err
	}
	return out, nil
}

// --- Previews (v0.12) ---

func (c *Client) ListPreviews(ctx context.Context, app string) (*api.ListPreviewsResponse, error) {
	out := &api.ListPreviewsResponse{}
	if err := c.do(ctx, "GET", "/v1/apps/"+url.PathEscape(app)+"/preview", nil, out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) CreatePreview(ctx context.Context, app, branch string) (*api.Preview, error) {
	out := &api.Preview{}
	if err := c.do(ctx, "POST", "/v1/apps/"+url.PathEscape(app)+"/preview/"+url.PathEscape(branch), nil, out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) DestroyPreview(ctx context.Context, app, branch string) error {
	return c.do(ctx, "DELETE", "/v1/apps/"+url.PathEscape(app)+"/preview/"+url.PathEscape(branch), nil, nil)
}

// --- GitHub webhook setup (v0.13) ---

func (c *Client) SetupGitHubWebhook(ctx context.Context, app string) (*api.WebhookSetupResponse, error) {
	out := &api.WebhookSetupResponse{}
	if err := c.do(ctx, "PUT", "/v1/webhooks/setup/github/"+url.PathEscape(app), nil, out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) GetGitHubWebhook(ctx context.Context, app string) (*api.WebhookSetupResponse, error) {
	out := &api.WebhookSetupResponse{}
	if err := c.do(ctx, "GET", "/v1/webhooks/setup/github/"+url.PathEscape(app), nil, out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) RemoveGitHubWebhook(ctx context.Context, app string) error {
	return c.do(ctx, "DELETE", "/v1/webhooks/setup/github/"+url.PathEscape(app), nil, nil)
}

// --- Storage (v0.14) ---

func (c *Client) ListStorage(ctx context.Context) (*api.ListStorageResponse, error) {
	out := &api.ListStorageResponse{}
	if err := c.do(ctx, "GET", "/v1/storage", nil, out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) CreateStorage(ctx context.Context, req *api.CreateStorageRequest) (*api.Storage, error) {
	out := &api.Storage{}
	if err := c.do(ctx, "POST", "/v1/storage", req, out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) GetStorage(ctx context.Context, name string) (*api.Storage, error) {
	out := &api.Storage{}
	if err := c.do(ctx, "GET", "/v1/storage/"+url.PathEscape(name), nil, out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) StorageURL(ctx context.Context, name string) (*api.StorageURL, error) {
	out := &api.StorageURL{}
	if err := c.do(ctx, "GET", "/v1/storage/"+url.PathEscape(name)+"/url", nil, out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) DestroyStorage(ctx context.Context, name string) error {
	return c.do(ctx, "DELETE", "/v1/storage/"+url.PathEscape(name), nil, nil)
}

// --- MySQL (v0.17) ---

func (c *Client) ListMySQL(ctx context.Context) (*api.ListMySQLResponse, error) {
	out := &api.ListMySQLResponse{}
	if err := c.do(ctx, "GET", "/v1/mysql", nil, out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) CreateMySQL(ctx context.Context, req *api.CreateMySQLRequest) (*api.MySQL, error) {
	out := &api.MySQL{}
	if err := c.do(ctx, "POST", "/v1/mysql", req, out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) GetMySQL(ctx context.Context, name string) (*api.MySQL, error) {
	out := &api.MySQL{}
	if err := c.do(ctx, "GET", "/v1/mysql/"+url.PathEscape(name), nil, out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) MySQLURL(ctx context.Context, name string) (string, error) {
	out := &api.MySQLURL{}
	if err := c.do(ctx, "GET", "/v1/mysql/"+url.PathEscape(name)+"/url", nil, out); err != nil {
		return "", err
	}
	return out.URL, nil
}

func (c *Client) DestroyMySQL(ctx context.Context, name string) error {
	return c.do(ctx, "DELETE", "/v1/mysql/"+url.PathEscape(name), nil, nil)
}

// --- ClickHouse (v0.17) ---

func (c *Client) ListClickHouse(ctx context.Context) (*api.ListClickHouseResponse, error) {
	out := &api.ListClickHouseResponse{}
	if err := c.do(ctx, "GET", "/v1/clickhouse", nil, out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) CreateClickHouse(ctx context.Context, req *api.CreateClickHouseRequest) (*api.ClickHouse, error) {
	out := &api.ClickHouse{}
	if err := c.do(ctx, "POST", "/v1/clickhouse", req, out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) GetClickHouse(ctx context.Context, name string) (*api.ClickHouse, error) {
	out := &api.ClickHouse{}
	if err := c.do(ctx, "GET", "/v1/clickhouse/"+url.PathEscape(name), nil, out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) ClickHouseURL(ctx context.Context, name string) (string, error) {
	out := &api.ClickHouseURL{}
	if err := c.do(ctx, "GET", "/v1/clickhouse/"+url.PathEscape(name)+"/url", nil, out); err != nil {
		return "", err
	}
	return out.URL, nil
}

func (c *Client) DestroyClickHouse(ctx context.Context, name string) error {
	return c.do(ctx, "DELETE", "/v1/clickhouse/"+url.PathEscape(name), nil, nil)
}

// --- MongoDB (v0.19) ---

func (c *Client) ListMongo(ctx context.Context) (*api.ListMongoResponse, error) {
	out := &api.ListMongoResponse{}
	if err := c.do(ctx, "GET", "/v1/mongodb", nil, out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) CreateMongo(ctx context.Context, req *api.CreateMongoRequest) (*api.Mongo, error) {
	out := &api.Mongo{}
	if err := c.do(ctx, "POST", "/v1/mongodb", req, out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) GetMongo(ctx context.Context, name string) (*api.Mongo, error) {
	out := &api.Mongo{}
	if err := c.do(ctx, "GET", "/v1/mongodb/"+url.PathEscape(name), nil, out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) MongoURL(ctx context.Context, name string) (string, error) {
	out := &api.MongoURL{}
	if err := c.do(ctx, "GET", "/v1/mongodb/"+url.PathEscape(name)+"/url", nil, out); err != nil {
		return "", err
	}
	return out.URL, nil
}

func (c *Client) DestroyMongo(ctx context.Context, name string) error {
	return c.do(ctx, "DELETE", "/v1/mongodb/"+url.PathEscape(name), nil, nil)
}

// --- ScyllaDB (v0.20) ---

func (c *Client) ListScylla(ctx context.Context) (*api.ListScyllaResponse, error) {
	out := &api.ListScyllaResponse{}
	if err := c.do(ctx, "GET", "/v1/scylladb", nil, out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) CreateScylla(ctx context.Context, req *api.CreateScyllaRequest) (*api.Scylla, error) {
	out := &api.Scylla{}
	if err := c.do(ctx, "POST", "/v1/scylladb", req, out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) GetScylla(ctx context.Context, name string) (*api.Scylla, error) {
	out := &api.Scylla{}
	if err := c.do(ctx, "GET", "/v1/scylladb/"+url.PathEscape(name), nil, out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) ScyllaURL(ctx context.Context, name string) (string, error) {
	out := &api.ScyllaURL{}
	if err := c.do(ctx, "GET", "/v1/scylladb/"+url.PathEscape(name)+"/url", nil, out); err != nil {
		return "", err
	}
	return out.URL, nil
}

func (c *Client) DestroyScylla(ctx context.Context, name string) error {
	return c.do(ctx, "DELETE", "/v1/scylladb/"+url.PathEscape(name), nil, nil)
}

// --- Custom-domain TLS bindings (v0.22) ---

func (c *Client) ListCerts(ctx context.Context) (*api.ListCertsResponse, error) {
	out := &api.ListCertsResponse{}
	if err := c.do(ctx, "GET", "/v1/certs", nil, out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) AddCert(ctx context.Context, app, hostname string) (*api.AddCertResponse, error) {
	out := &api.AddCertResponse{}
	req := &api.AddCertRequest{App: app, Hostname: hostname}
	if err := c.do(ctx, "POST", "/v1/certs", req, out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) GetCert(ctx context.Context, hostname string) (*api.CertBinding, error) {
	out := &api.CertBinding{}
	if err := c.do(ctx, "GET", "/v1/certs/"+url.PathEscape(hostname), nil, out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) VerifyCert(ctx context.Context, hostname string) (*api.VerifyCertResponse, error) {
	out := &api.VerifyCertResponse{}
	if err := c.do(ctx, "POST", "/v1/certs/"+url.PathEscape(hostname)+"/verify", nil, out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) RemoveCert(ctx context.Context, hostname string) error {
	return c.do(ctx, "DELETE", "/v1/certs/"+url.PathEscape(hostname), nil, nil)
}

// --- Jobs (v0.23) ---

func (c *Client) RunJob(ctx context.Context, req *api.RunJobRequest) (*api.JobRun, error) {
	out := &api.JobRun{}
	if err := c.do(ctx, "POST", "/v1/jobs/run", req, out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) ScheduleJob(ctx context.Context, req *api.ScheduleJobRequest) (*api.JobRun, error) {
	out := &api.JobRun{}
	if err := c.do(ctx, "POST", "/v1/jobs/schedule", req, out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) ListJobs(ctx context.Context) (*api.ListJobsResponse, error) {
	out := &api.ListJobsResponse{}
	if err := c.do(ctx, "GET", "/v1/jobs", nil, out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) StatusJob(ctx context.Context, id string) (*api.JobRun, error) {
	out := &api.JobRun{}
	if err := c.do(ctx, "GET", "/v1/jobs/"+url.PathEscape(id), nil, out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) JobLogs(ctx context.Context, id string, fire int) (*api.JobLogsResponse, error) {
	out := &api.JobLogsResponse{}
	q := ""
	if fire > 0 {
		q = "?fire=" + fmt.Sprintf("%d", fire)
	}
	if err := c.do(ctx, "GET", "/v1/jobs/"+url.PathEscape(id)+"/logs"+q, nil, out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) CancelJob(ctx context.Context, id string) error {
	return c.do(ctx, "DELETE", "/v1/jobs/"+url.PathEscape(id), nil, nil)
}
