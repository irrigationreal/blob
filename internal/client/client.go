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
