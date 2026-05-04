package server

import "testing"

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
