package server

import (
	"testing"

	"github.com/irrigationreal/blob/internal/api"
)

func TestAllocationReservationPrefersAllocatedResources(t *testing.T) {
	alloc := nomadAllocation{Resources: nomadAllocationResource{CPU: 999, MemoryMB: 999, DiskMB: 999}}
	alloc.AllocatedResources.Shared.DiskMB = 300
	alloc.AllocatedResources.Tasks = map[string]struct {
		Cpu struct {
			CpuShares int `json:"CpuShares"`
		} `json:"Cpu"`
		Memory struct {
			MemoryMB int `json:"MemoryMB"`
		} `json:"Memory"`
	}{
		"app":     {},
		"sidecar": {},
	}
	app := alloc.AllocatedResources.Tasks["app"]
	app.Cpu.CpuShares = 200
	app.Memory.MemoryMB = 256
	alloc.AllocatedResources.Tasks["app"] = app
	sidecar := alloc.AllocatedResources.Tasks["sidecar"]
	sidecar.Cpu.CpuShares = 50
	sidecar.Memory.MemoryMB = 64
	alloc.AllocatedResources.Tasks["sidecar"] = sidecar

	cpu, memory, disk := allocationReservation(alloc)
	if cpu != 250 || memory != 320 || disk != 300 {
		t.Fatalf("reservation = cpu=%d memory=%d disk=%d; want 250/320/300", cpu, memory, disk)
	}
}

func TestRecommendPlacementReportsMemoryRemediation(t *testing.T) {
	graph := &api.ResourceGraph{Nodes: []api.Node{{
		Name:      "platform",
		Status:    "ready",
		Eligible:  "eligible",
		Resources: api.NodeResources{MemoryMB: api.ResourceUsage{Total: 24042, Reserved: 22368, Available: 1674}},
	}}}
	rec := recommendPlacement(100, 99999, 300, graph)
	if rec.Fits {
		t.Fatal("oversized memory request should not fit")
	}
	want := "needs 99999 MiB memory; largest eligible node platform has 1674 MiB available (22368 MiB reserved of 24042 MiB)"
	if rec.Detail != want {
		t.Fatalf("detail = %q; want %q", rec.Detail, want)
	}
}

func TestRecommendPlacementFitsNode(t *testing.T) {
	graph := &api.ResourceGraph{Nodes: []api.Node{{
		Name:     "platform",
		Status:   "ready",
		Eligible: "eligible",
		Resources: api.NodeResources{
			CPU:      api.ResourceUsage{Available: 1000, Total: 2000},
			MemoryMB: api.ResourceUsage{Available: 2048, Total: 4096},
			DiskMB:   api.ResourceUsage{Available: 10000, Total: 20000},
		},
	}}}
	rec := recommendPlacement(200, 512, 300, graph)
	if !rec.Fits || rec.Node.Name != "platform" {
		t.Fatalf("expected request to fit on platform, got %+v", rec)
	}
}

func TestDeployRequestReservationUsesPerAllocationShape(t *testing.T) {
	cpu, memory, disk := deployRequestReservation(&api.DeployRequest{
		CPU:      100,
		Memory:   128,
		Replicas: 3,
		Sidecars: []api.Sidecar{{CPU: 25, Memory: 64}},
	})
	if cpu != 125 || memory != 192 || disk != 300 {
		t.Fatalf("reservation shape = cpu=%d memory=%d disk=%d; want 125/192/300", cpu, memory, disk)
	}
}

func TestRecommendPlacementForCountSpreadsReplicasAcrossNodes(t *testing.T) {
	graph := &api.ResourceGraph{Nodes: []api.Node{
		{
			Name:     "node-a",
			Status:   "ready",
			Eligible: "eligible",
			Resources: api.NodeResources{
				CPU:      api.ResourceUsage{Available: 500, Total: 500},
				MemoryMB: api.ResourceUsage{Available: 600, Total: 600},
				DiskMB:   api.ResourceUsage{Available: 1000, Total: 1000},
			},
		},
		{
			Name:     "node-b",
			Status:   "ready",
			Eligible: "eligible",
			Resources: api.NodeResources{
				CPU:      api.ResourceUsage{Available: 500, Total: 500},
				MemoryMB: api.ResourceUsage{Available: 600, Total: 600},
				DiskMB:   api.ResourceUsage{Available: 1000, Total: 1000},
			},
		},
	}}
	rec := recommendPlacementForCount(400, 512, 300, 2, graph)
	if !rec.Fits {
		t.Fatalf("expected two replicas to fit by spreading across nodes, got %+v", rec)
	}
}

func TestAllocReservesCapacitySkipsTerminalAllocations(t *testing.T) {
	if !allocReservesCapacity(nomadAllocation{DesiredStatus: "run", ClientStatus: "running"}) {
		t.Fatal("running allocation should reserve capacity")
	}
	if allocReservesCapacity(nomadAllocation{DesiredStatus: "run", ClientStatus: "complete"}) {
		t.Fatal("complete allocation should not reserve capacity")
	}
	if allocReservesCapacity(nomadAllocation{DesiredStatus: "stop", ClientStatus: "running"}) {
		t.Fatal("stop-desired allocation should not reserve capacity")
	}
}
