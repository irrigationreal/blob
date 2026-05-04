package server

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/darvell/blob/internal/api"
)

func (s *Server) costsDir() string {
	return filepath.Join(s.cfg.StateDir, "costs")
}

func (s *Server) costsSnapshotPath() string {
	return filepath.Join(s.costsDir(), "latest.json")
}

func (s *Server) handleCosts(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		writeErr(w, 405, "method not allowed")
		return
	}
	view := strings.Trim(strings.TrimPrefix(r.URL.Path, "/v1/costs"), "/")
	monthlyUSD, _ := strconv.ParseFloat(r.URL.Query().Get("monthly_usd"), 64)
	snap, err := s.collectCostSnapshot(r.Context(), monthlyUSD)
	if err != nil {
		if cached, ok := s.loadCostSnapshot(); ok {
			snap = cached
			applyMonthlyEstimate(snap, monthlyUSD)
		} else {
			writeErr(w, 500, err.Error())
			return
		}
	}
	switch view {
	case "", "summary":
		writeJSON(w, 200, snap)
	case "apps":
		writeJSON(w, 200, map[string]any{"generated_at": snap.GeneratedAt, "apps": snap.Apps})
	case "nodes":
		writeJSON(w, 200, map[string]any{"generated_at": snap.GeneratedAt, "nodes": snap.Nodes})
	default:
		writeErr(w, 404, "not found")
	}
}

func (s *Server) collectCostSnapshot(ctx context.Context, monthlyUSD float64) (*api.CostSnapshot, error) {
	graph, err := s.collectResourceGraph(ctx)
	if err != nil {
		return nil, err
	}
	nodeNames := map[string]string{}
	nodeCosts := make([]api.CostNode, 0, len(graph.Nodes))
	summary := api.CostSummary{GeneratedAt: graph.GeneratedAt, NodeCount: len(graph.Nodes)}
	for _, n := range graph.Nodes {
		nodeNames[n.ID] = n.Name
		nodeCosts = append(nodeCosts, api.CostNode{
			ID:                n.ID,
			Name:              n.Name,
			Address:           n.Address,
			Datacenter:        n.Datacenter,
			Status:            n.Status,
			Eligible:          n.Eligible,
			CPU:               n.Resources.CPU,
			MemoryMB:          n.Resources.MemoryMB,
			DiskMB:            n.Resources.DiskMB,
			ActiveAllocations: n.ActiveAllocations,
		})
		summary.CPU.Total += n.Resources.CPU.Total
		summary.CPU.Reserved += n.Resources.CPU.Reserved
		summary.CPU.Available += n.Resources.CPU.Available
		summary.MemoryMB.Total += n.Resources.MemoryMB.Total
		summary.MemoryMB.Reserved += n.Resources.MemoryMB.Reserved
		summary.MemoryMB.Available += n.Resources.MemoryMB.Available
		summary.DiskMB.Total += n.Resources.DiskMB.Total
		summary.DiskMB.Reserved += n.Resources.DiskMB.Reserved
		summary.DiskMB.Available += n.Resources.DiskMB.Available
		summary.ActiveAllocations += n.ActiveAllocations
	}

	appByID := map[string]*api.CostApp{}
	appNodeSets := map[string]map[string]struct{}{}
	for _, n := range graph.Nodes {
		allocBody, err := s.nomadGET(ctx, "/v1/node/"+n.ID+"/allocations")
		if err != nil {
			return nil, err
		}
		var allocs []nomadAllocation
		if err := json.Unmarshal(allocBody, &allocs); err != nil {
			return nil, err
		}
		for _, alloc := range allocs {
			if !allocReservesCapacity(alloc) {
				continue
			}
			appID := alloc.JobID
			if appID == "" {
				continue
			}
			app, ok := appByID[appID]
			if !ok {
				name, env := splitJobID(appID)
				app = &api.CostApp{App: appID, Environment: env}
				if meta, ok := s.loadJobMeta(appID); ok && meta.App != "" {
					app.App = meta.App
					app.Environment = meta.Environment
				} else if env == "" {
					app.App = name
				}
				appByID[appID] = app
				appNodeSets[appID] = map[string]struct{}{}
			}
			cpu, memory, disk := allocationReservation(alloc)
			app.CPU += cpu
			app.MemoryMB += memory
			app.DiskMB += disk
			app.Allocations++
			if node := nodeNames[alloc.NodeID]; node != "" {
				appNodeSets[appID][node] = struct{}{}
			}
		}
	}

	apps := make([]api.CostApp, 0, len(appByID))
	for id, app := range appByID {
		for node := range appNodeSets[id] {
			app.Nodes = append(app.Nodes, node)
		}
		sort.Strings(app.Nodes)
		apps = append(apps, *app)
	}
	sort.Slice(apps, func(i, j int) bool {
		if apps[i].MemoryMB == apps[j].MemoryMB {
			return apps[i].App < apps[j].App
		}
		return apps[i].MemoryMB > apps[j].MemoryMB
	})
	sort.Slice(nodeCosts, func(i, j int) bool { return nodeCosts[i].Name < nodeCosts[j].Name })
	summary.AppCount = len(apps)

	snap := &api.CostSnapshot{GeneratedAt: graph.GeneratedAt, Summary: summary, Apps: apps, Nodes: nodeCosts}
	applyMonthlyEstimate(snap, monthlyUSD)
	if err := s.saveCostSnapshot(snap); err != nil {
		return nil, err
	}
	return snap, nil
}

func applyMonthlyEstimate(snap *api.CostSnapshot, monthlyUSD float64) {
	if snap == nil {
		return
	}
	snap.Summary.MonthlyEstimateUSD = 0
	for i := range snap.Apps {
		snap.Apps[i].MonthlyEstimateUSD = 0
	}
	for i := range snap.Nodes {
		snap.Nodes[i].MonthlyEstimateUSD = 0
	}
	if monthlyUSD <= 0 {
		return
	}
	snap.Summary.MonthlyEstimateUSD = monthlyUSD
	reservedMemory := snap.Summary.MemoryMB.Reserved
	if reservedMemory <= 0 {
		return
	}
	for i := range snap.Apps {
		snap.Apps[i].MonthlyEstimateUSD = monthlyUSD * float64(snap.Apps[i].MemoryMB) / float64(reservedMemory)
	}
	fleetMemory := snap.Summary.MemoryMB.Total
	if fleetMemory <= 0 {
		return
	}
	for i := range snap.Nodes {
		snap.Nodes[i].MonthlyEstimateUSD = monthlyUSD * float64(snap.Nodes[i].MemoryMB.Total) / float64(fleetMemory)
	}
}

func (s *Server) saveCostSnapshot(snap *api.CostSnapshot) error {
	if err := os.MkdirAll(s.costsDir(), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.costsSnapshotPath(), b, 0o644)
}

func (s *Server) loadCostSnapshot() (*api.CostSnapshot, bool) {
	b, err := os.ReadFile(s.costsSnapshotPath())
	if err != nil {
		return nil, false
	}
	var snap api.CostSnapshot
	if err := json.Unmarshal(b, &snap); err != nil {
		return nil, false
	}
	if snap.GeneratedAt.IsZero() {
		snap.GeneratedAt = time.Now().UTC()
	}
	return &snap, true
}
