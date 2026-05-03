package api

import "time"

type DeployRequest struct {
	App     string            `json:"app"`
	Domain  string            `json:"domain,omitempty"`
	Port    int               `json:"port,omitempty"`
	Tag     string            `json:"tag,omitempty"`
	CPU     int               `json:"cpu,omitempty"`
	Memory  int               `json:"memory,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	Form     string            `json:"form,omitempty"` // web-service | daemon
	Replicas int               `json:"replicas,omitempty"`
}

type DeployResponse struct {
	App       string    `json:"app"`
	Domain    string    `json:"domain"`
	Image     string    `json:"image"`
	URL       string    `json:"url"`
	JobID     string    `json:"job_id"`
	StartedAt time.Time `json:"started_at"`
	Phases    []Phase   `json:"phases"`
}

type Phase struct {
	Name       string        `json:"name"`
	DurationMS int64         `json:"duration_ms"`
	OK         bool          `json:"ok"`
	Note       string        `json:"note,omitempty"`
	When       time.Time     `json:"when"`
	took       time.Duration `json:"-"`
}

type AppSummary struct {
	App       string    `json:"app"`
	Domain    string    `json:"domain"`
	Image     string    `json:"image"`
	URL       string    `json:"url"`
	Status    string    `json:"status"`
	Form      string    `json:"form"`
	Replicas  int       `json:"replicas"`
	UpdatedAt time.Time `json:"updated_at"`
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
