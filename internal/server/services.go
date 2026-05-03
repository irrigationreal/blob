// Package server: managed-services rollup endpoint (v0.11).
//
// /v1/services fans out to every managed-service registry on disk and
// returns a single sorted summary. No new state — this is a pure
// aggregator over postgres / valkey / loki / grafana / promtail /
// nats / tempo / prometheus.
package server

import (
	"context"
	"fmt"
	"net/http"
	"sort"

	"github.com/darvell/blob/internal/api"
)

func (s *Server) handleServices(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		writeErr(w, 405, "method not allowed")
		return
	}
	out, err := s.listServices(r.Context())
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, out)
}

func (s *Server) listServices(ctx context.Context) (*api.ListServicesResponse, error) {
	out := &api.ListServicesResponse{}

	if pg, err := s.listPostgres(ctx); err == nil {
		for _, m := range pg.Postgres {
			out.Services = append(out.Services, api.ServiceSummary{
				Kind: "postgres", Name: m.Name, Status: m.Status, Host: m.Host,
				Ports: []int{m.Port},
				URLs:  []string{m.URLMasked},
			})
		}
	}
	if vk, err := s.listValkey(ctx); err == nil {
		for _, m := range vk.Valkey {
			out.Services = append(out.Services, api.ServiceSummary{
				Kind: "valkey", Name: m.Name, Status: m.Status, Host: m.Host,
				Ports: []int{m.Port},
				URLs:  []string{m.URLMasked},
			})
		}
	}
	if lk, err := s.listLoki(ctx); err == nil {
		for _, m := range lk.Loki {
			out.Services = append(out.Services, api.ServiceSummary{
				Kind: "loki", Name: m.Name, Status: m.Status, Host: m.Host,
				Ports: []int{m.Port}, URLs: []string{m.URL},
			})
		}
	}
	if gf, err := s.listGrafana(ctx); err == nil {
		for _, m := range gf.Grafana {
			out.Services = append(out.Services, api.ServiceSummary{
				Kind: "grafana", Name: m.Name, Status: m.Status, Host: m.Host,
				Ports: []int{m.Port}, URLs: []string{m.URL},
			})
		}
	}
	if pt, err := s.listPromtail(ctx); err == nil {
		for _, m := range pt.Promtail {
			out.Services = append(out.Services, api.ServiceSummary{
				Kind: "promtail", Name: m.Name, Status: m.Status,
				URLs: []string{m.LokiURL},
			})
		}
	}
	if nt, err := s.listNATS(ctx); err == nil {
		for _, m := range nt.NATS {
			out.Services = append(out.Services, api.ServiceSummary{
				Kind: "nats", Name: m.Name, Status: m.Status, Host: m.Host,
				Ports: []int{m.Port}, URLs: []string{m.URL},
			})
		}
	}
	if tp, err := s.listTempo(ctx); err == nil {
		for _, m := range tp.Tempo {
			out.Services = append(out.Services, api.ServiceSummary{
				Kind: "tempo", Name: m.Name, Status: m.Status, Host: m.Host,
				Ports: []int{m.HTTPPort, m.OTLPPort},
				URLs:  []string{m.URL, "otlp:" + m.OTLPGRPC},
			})
		}
	}
	if pr, err := s.listPrometheus(ctx); err == nil {
		for _, m := range pr.Prometheus {
			out.Services = append(out.Services, api.ServiceSummary{
				Kind: "prometheus", Name: m.Name, Status: m.Status, Host: m.Host,
				Ports: []int{m.Port}, URLs: []string{m.URL},
			})
		}
	}

	sort.Slice(out.Services, func(i, j int) bool {
		if out.Services[i].Kind != out.Services[j].Kind {
			return out.Services[i].Kind < out.Services[j].Kind
		}
		return out.Services[i].Name < out.Services[j].Name
	})
	return out, nil
}

// portsString formats Ports for CLI output.
func portsString(ports []int) string {
	if len(ports) == 0 {
		return ""
	}
	s := ""
	for i, p := range ports {
		if i > 0 {
			s += ","
		}
		s += fmt.Sprintf("%d", p)
	}
	return s
}
