package server

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/irrigationreal/blob/internal/api"
)

const defaultEphemeralDiskMB = 300

func (s *Server) resourceGraphPath() string {
	return filepath.Join(s.cfg.StateDir, "resource-graph.json")
}

type nomadNodeDetail struct {
	ID                    string            `json:"ID"`
	Name                  string            `json:"Name"`
	Datacenter            string            `json:"Datacenter"`
	Status                string            `json:"Status"`
	NodeClass             string            `json:"NodeClass"`
	Address               string            `json:"Address"`
	SchedulingEligibility string            `json:"SchedulingEligibility"`
	Drain                 bool              `json:"Drain"`
	Attributes            map[string]string `json:"Attributes"`
	NodeResources         struct {
		Cpu struct {
			CpuShares int `json:"CpuShares"`
		} `json:"Cpu"`
		Memory struct {
			MemoryMB int `json:"MemoryMB"`
		} `json:"Memory"`
		Disk struct {
			DiskMB int `json:"DiskMB"`
		} `json:"Disk"`
	} `json:"NodeResources"`
	ReservedResources struct {
		Cpu struct {
			CpuShares int `json:"CpuShares"`
		} `json:"Cpu"`
		Memory struct {
			MemoryMB int `json:"MemoryMB"`
		} `json:"Memory"`
		Disk struct {
			DiskMB int `json:"DiskMB"`
		} `json:"Disk"`
	} `json:"ReservedResources"`
}

type nomadAllocationResource struct {
	CPU         int `json:"CPU"`
	MemoryMB    int `json:"MemoryMB"`
	MemoryMaxMB int `json:"MemoryMaxMB"`
	DiskMB      int `json:"DiskMB"`
}

type nomadAllocation struct {
	ID            string                  `json:"ID"`
	JobID         string                  `json:"JobID"`
	NodeID        string                  `json:"NodeID"`
	ClientStatus  string                  `json:"ClientStatus"`
	DesiredStatus string                  `json:"DesiredStatus"`
	Resources     nomadAllocationResource `json:"Resources"`
	TaskResources map[string]struct {
		CPU      int `json:"CPU"`
		MemoryMB int `json:"MemoryMB"`
		DiskMB   int `json:"DiskMB"`
	} `json:"TaskResources"`
	AllocatedResources struct {
		Shared struct {
			DiskMB int `json:"DiskMB"`
		} `json:"Shared"`
		Tasks map[string]struct {
			Cpu struct {
				CpuShares int `json:"CpuShares"`
			} `json:"Cpu"`
			Memory struct {
				MemoryMB int `json:"MemoryMB"`
			} `json:"Memory"`
		} `json:"Tasks"`
	} `json:"AllocatedResources"`
}

func (s *Server) collectResourceGraph(ctx context.Context) (*api.ResourceGraph, error) {
	body, err := s.nomadGET(ctx, "/v1/nodes")
	if err != nil {
		return nil, err
	}
	var nodes []struct {
		ID                    string `json:"ID"`
		Name                  string `json:"Name"`
		Address               string `json:"Address"`
		Datacenter            string `json:"Datacenter"`
		Status                string `json:"Status"`
		NodeClass             string `json:"NodeClass"`
		SchedulingEligibility string `json:"SchedulingEligibility"`
		Drain                 bool   `json:"Drain"`
	}
	if err := json.Unmarshal(body, &nodes); err != nil {
		return nil, err
	}

	graph := &api.ResourceGraph{GeneratedAt: time.Now().UTC()}
	for _, n := range nodes {
		detailBody, err := s.nomadGET(ctx, "/v1/node/"+n.ID)
		if err != nil {
			return nil, err
		}
		var detail nomadNodeDetail
		if err := json.Unmarshal(detailBody, &detail); err != nil {
			return nil, err
		}
		node := api.Node{
			ID:         firstNonEmpty(detail.ID, n.ID),
			Name:       firstNonEmpty(detail.Name, n.Name),
			Address:    firstNonEmpty(detail.Address, n.Address),
			Datacenter: firstNonEmpty(detail.Datacenter, n.Datacenter),
			Status:     firstNonEmpty(detail.Status, n.Status),
			Eligible:   firstNonEmpty(detail.SchedulingEligibility, n.SchedulingEligibility),
			Drain:      detail.Drain || n.Drain,
			NodeClass:  firstNonEmpty(detail.NodeClass, n.NodeClass),
			Labels:     resourceLabels(detail.Attributes),
			Resources: api.NodeResources{
				CPU: api.ResourceUsage{
					Total:    detail.NodeResources.Cpu.CpuShares,
					Reserved: detail.ReservedResources.Cpu.CpuShares,
				},
				MemoryMB: api.ResourceUsage{
					Total:    detail.NodeResources.Memory.MemoryMB,
					Reserved: detail.ReservedResources.Memory.MemoryMB,
				},
				DiskMB: api.ResourceUsage{
					Total:    detail.NodeResources.Disk.DiskMB,
					Reserved: detail.ReservedResources.Disk.DiskMB,
				},
			},
		}
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
			cpu, memory, disk := allocationReservation(alloc)
			node.Resources.CPU.Reserved += cpu
			node.Resources.MemoryMB.Reserved += memory
			node.Resources.DiskMB.Reserved += disk
			node.ActiveAllocations++
		}
		node.Resources.CPU.Available = nonNegative(node.Resources.CPU.Total - node.Resources.CPU.Reserved)
		node.Resources.MemoryMB.Available = nonNegative(node.Resources.MemoryMB.Total - node.Resources.MemoryMB.Reserved)
		node.Resources.DiskMB.Available = nonNegative(node.Resources.DiskMB.Total - node.Resources.DiskMB.Reserved)
		graph.Nodes = append(graph.Nodes, node)
	}
	sort.Slice(graph.Nodes, func(i, j int) bool { return graph.Nodes[i].Name < graph.Nodes[j].Name })
	if err := s.saveResourceGraph(graph); err != nil {
		return nil, err
	}
	return graph, nil
}

func (s *Server) saveResourceGraph(graph *api.ResourceGraph) error {
	if err := os.MkdirAll(s.cfg.StateDir, 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(graph, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.resourceGraphPath(), b, 0o644)
}

func (s *Server) loadResourceGraph() (*api.ResourceGraph, bool) {
	b, err := os.ReadFile(s.resourceGraphPath())
	if err != nil {
		return nil, false
	}
	var graph api.ResourceGraph
	if err := json.Unmarshal(b, &graph); err != nil {
		return nil, false
	}
	return &graph, true
}

func resourceLabels(attrs map[string]string) map[string]string {
	labels := map[string]string{}
	if arch := attrs["cpu.arch"]; arch != "" {
		labels["arch"] = arch
	}
	if runtimes := attrs["driver.docker.runtimes"]; strings.Contains(runtimes, "kata-runtime") {
		labels["kata"] = "true"
	}
	if docker := attrs["driver.docker"]; docker != "" {
		labels["docker"] = docker
	}
	if len(labels) == 0 {
		return nil
	}
	return labels
}

func allocReservesCapacity(alloc nomadAllocation) bool {
	if alloc.DesiredStatus != "run" {
		return false
	}
	switch alloc.ClientStatus {
	case "complete", "failed", "lost", "dead":
		return false
	default:
		return true
	}
}

func allocationReservation(alloc nomadAllocation) (cpu, memory, disk int) {
	for _, task := range alloc.AllocatedResources.Tasks {
		cpu += task.Cpu.CpuShares
		memory += task.Memory.MemoryMB
	}
	disk = alloc.AllocatedResources.Shared.DiskMB
	if cpu == 0 {
		cpu = alloc.Resources.CPU
	}
	if memory == 0 {
		memory = alloc.Resources.MemoryMB
	}
	if disk == 0 {
		disk = alloc.Resources.DiskMB
	}
	if cpu == 0 || memory == 0 {
		for _, task := range alloc.TaskResources {
			if cpu == 0 {
				cpu += task.CPU
			}
			if memory == 0 {
				memory += task.MemoryMB
			}
		}
	}
	return cpu, memory, disk
}

func nonNegative(n int) int {
	if n < 0 {
		return 0
	}
	return n
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func (s *Server) preflightPlacement(ctx context.Context, req *api.DeployRequest) error {
	cpu, memory, disk := deployRequestReservation(req)
	count := deployRequestAllocationCount(req)
	graph, err := s.collectResourceGraph(ctx)
	if err != nil {
		if cached, ok := s.loadResourceGraph(); ok {
			graph = cached
		} else {
			return fmt.Errorf("placement preflight: resource graph unavailable: %w", err)
		}
	}
	rec := recommendPlacementForCount(cpu, memory, disk, count, graph)
	if rec.Fits {
		return nil
	}
	if rec.Remediate != "" {
		return fmt.Errorf("placement preflight: %s; %s", rec.Detail, rec.Remediate)
	}
	return fmt.Errorf("placement preflight: %s", rec.Detail)
}

func deployRequestReservation(req *api.DeployRequest) (cpu, memory, disk int) {
	cpu = req.CPU
	memory = req.Memory
	for _, sidecar := range req.Sidecars {
		if sidecar.CPU > 0 {
			cpu += sidecar.CPU
		} else {
			cpu += 100
		}
		if sidecar.Memory > 0 {
			memory += sidecar.Memory
		} else {
			memory += 128
		}
	}
	return cpu, memory, defaultEphemeralDiskMB
}

func deployRequestAllocationCount(req *api.DeployRequest) int {
	if req.Replicas > 0 {
		return req.Replicas
	}
	return 1
}

func recommendPlacement(cpu, memory, disk int, graph *api.ResourceGraph) api.PlacementRecommendation {
	rec := api.PlacementRecommendation{CPU: cpu, MemoryMB: memory, DiskMB: disk}
	if graph != nil {
		rec.GeneratedAt = graph.GeneratedAt
	}
	var fit, memoryBest, cpuBest, diskBest *api.Node
	if graph != nil {
		for i := range graph.Nodes {
			n := &graph.Nodes[i]
			if !nodeAcceptsPlacement(n) {
				continue
			}
			if memoryBest == nil || n.Resources.MemoryMB.Available > memoryBest.Resources.MemoryMB.Available {
				memoryBest = n
			}
			if cpuBest == nil || n.Resources.CPU.Available > cpuBest.Resources.CPU.Available {
				cpuBest = n
			}
			if diskBest == nil || n.Resources.DiskMB.Available > diskBest.Resources.DiskMB.Available {
				diskBest = n
			}
			if n.Resources.MemoryMB.Available >= memory && n.Resources.CPU.Available >= cpu && n.Resources.DiskMB.Available >= disk {
				if fit == nil || n.Resources.MemoryMB.Available > fit.Resources.MemoryMB.Available {
					fit = n
				}
			}
		}
	}
	if fit != nil {
		rec.Fits = true
		rec.Node = *fit
		rec.Detail = fmt.Sprintf("fits on %s with %d MiB memory, %d CPU shares, and %d MiB disk still available after placement", fit.Name, fit.Resources.MemoryMB.Available-memory, fit.Resources.CPU.Available-cpu, fit.Resources.DiskMB.Available-disk)
		return rec
	}
	if memoryBest == nil {
		rec.Detail = "no ready eligible nodes are available"
		rec.Remediate = "Add a ready node or run `blob nodes undrain <id>` on a drained node"
		return rec
	}
	if memory > 0 && memory > memoryBest.Resources.MemoryMB.Available {
		rec.Node = *memoryBest
		rec.Detail = fmt.Sprintf("needs %d MiB memory; largest eligible node %s has %d MiB available (%d MiB reserved of %d MiB)", memory, memoryBest.Name, memoryBest.Resources.MemoryMB.Available, memoryBest.Resources.MemoryMB.Reserved, memoryBest.Resources.MemoryMB.Total)
		rec.Remediate = "Lower the workload memory request or add/undrain a node with more free RAM"
		return rec
	}
	if cpu > 0 && cpu > cpuBest.Resources.CPU.Available {
		rec.Node = *cpuBest
		rec.Detail = fmt.Sprintf("needs %d CPU shares; largest eligible node %s has %d available (%d reserved of %d)", cpu, cpuBest.Name, cpuBest.Resources.CPU.Available, cpuBest.Resources.CPU.Reserved, cpuBest.Resources.CPU.Total)
		rec.Remediate = "Lower the workload CPU request or add/undrain capacity"
		return rec
	}
	if disk > 0 && disk > diskBest.Resources.DiskMB.Available {
		rec.Node = *diskBest
		rec.Detail = fmt.Sprintf("needs %d MiB disk; largest eligible node %s has %d MiB available (%d MiB reserved of %d MiB)", disk, diskBest.Name, diskBest.Resources.DiskMB.Available, diskBest.Resources.DiskMB.Reserved, diskBest.Resources.DiskMB.Total)
		rec.Remediate = "Free disk or add/undrain a node with more disk capacity"
		return rec
	}
	rec.Node = *memoryBest
	rec.Detail = fmt.Sprintf("largest eligible node %s has cpu %d/%d available and memory %d/%d MiB available; check constraints, port conflicts, image pull errors, or node runtime labels", memoryBest.Name, memoryBest.Resources.CPU.Available, memoryBest.Resources.CPU.Total, memoryBest.Resources.MemoryMB.Available, memoryBest.Resources.MemoryMB.Total)
	rec.Remediate = "Run `nomad eval status` on the platform for the scheduler's exact constraint failure"
	return rec
}

func recommendPlacementForCount(cpu, memory, disk, count int, graph *api.ResourceGraph) api.PlacementRecommendation {
	if count <= 1 {
		return recommendPlacement(cpu, memory, disk, graph)
	}
	rec := api.PlacementRecommendation{CPU: cpu * count, MemoryMB: memory * count, DiskMB: disk * count}
	if graph != nil {
		rec.GeneratedAt = graph.GeneratedAt
	}

	single := recommendPlacement(cpu, memory, disk, graph)
	if !single.Fits {
		return single
	}

	slots, nodesWithRoom := 0, 0
	var best *api.Node
	if graph != nil {
		for i := range graph.Nodes {
			n := &graph.Nodes[i]
			if !nodeAcceptsPlacement(n) {
				continue
			}
			nodeSlots := placementSlots(n, cpu, memory, disk)
			if nodeSlots > 0 {
				nodesWithRoom++
				slots += nodeSlots
				if best == nil || nodeSlots > placementSlots(best, cpu, memory, disk) {
					best = n
				}
			}
		}
	}
	if slots >= count {
		rec.Fits = true
		if best != nil {
			rec.Node = *best
		}
		rec.Detail = fmt.Sprintf("fits %d allocations across %d eligible nodes; each allocation needs %d MiB memory, %d CPU shares, and %d MiB disk", count, nodesWithRoom, memory, cpu, disk)
		return rec
	}
	if best != nil {
		rec.Node = *best
	}
	rec.Detail = fmt.Sprintf("needs %d allocations of %d MiB memory, %d CPU shares, and %d MiB disk; eligible fleet currently has room for %d", count, memory, cpu, disk, slots)
	rec.Remediate = "Lower replicas or resource requests, or add/undrain capacity"
	return rec
}

func nodeAcceptsPlacement(n *api.Node) bool {
	return n.Status == "ready" && n.Eligible == "eligible" && !n.Drain
}

func placementSlots(n *api.Node, cpu, memory, disk int) int {
	const maxInt = int(^uint(0) >> 1)
	slots := maxInt
	if cpu > 0 {
		slots = minInt(slots, n.Resources.CPU.Available/cpu)
	}
	if memory > 0 {
		slots = minInt(slots, n.Resources.MemoryMB.Available/memory)
	}
	if disk > 0 {
		slots = minInt(slots, n.Resources.DiskMB.Available/disk)
	}
	if slots == maxInt {
		return 0
	}
	return slots
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func (s *Server) placementRemediation(ctx context.Context, jobID string, graph *api.ResourceGraph) (string, string) {
	cpu, memory, disk := s.pendingJobReservation(ctx, jobID)
	rec := recommendPlacement(cpu, memory, disk, graph)
	return rec.Detail, rec.Remediate
}

func (s *Server) pendingJobReservation(ctx context.Context, jobID string) (cpu, memory, disk int) {
	body, err := s.nomadGET(ctx, "/v1/job/"+jobID+"/allocations")
	if err == nil {
		var allocs []nomadAllocation
		if json.Unmarshal(body, &allocs) == nil {
			for _, alloc := range allocs {
				if alloc.DesiredStatus == "run" && alloc.ClientStatus == "pending" {
					return allocationReservation(alloc)
				}
			}
		}
	}

	body, err = s.nomadGET(ctx, "/v1/job/"+jobID)
	if err != nil {
		return 0, 0, 0
	}
	var job struct {
		TaskGroups []struct {
			Count         int
			EphemeralDisk struct {
				SizeMB int
			}
			Tasks []struct {
				Resources nomadAllocationResource
			}
		}
	}
	if err := json.Unmarshal(body, &job); err != nil {
		return 0, 0, 0
	}
	for _, group := range job.TaskGroups {
		count := group.Count
		if count <= 0 {
			count = 1
		}
		groupCPU, groupMemory, groupDisk := 0, 0, group.EphemeralDisk.SizeMB
		for _, task := range group.Tasks {
			groupCPU += task.Resources.CPU
			groupMemory += task.Resources.MemoryMB
			groupDisk += task.Resources.DiskMB
		}
		cpu += groupCPU * count
		memory += groupMemory * count
		disk += groupDisk * count
	}
	return cpu, memory, disk
}
