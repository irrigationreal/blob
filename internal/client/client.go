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
	out := &api.LogsResponse{}
	if err := c.do(ctx, "GET", fmt.Sprintf("/v1/apps/%s/logs?lines=%d", url.PathEscape(app), lines), nil, out); err != nil {
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

func (c *Client) ListNodes(ctx context.Context) (*api.ListNodesResponse, error) {
	out := &api.ListNodesResponse{}
	if err := c.do(ctx, "GET", "/v1/nodes", nil, out); err != nil {
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
