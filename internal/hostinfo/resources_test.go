package hostinfo

import "testing"

func TestCPUTimes(t *testing.T) {
	total, idle := cpuTimes("cpu 100 20 30 200 10 5 7 8 50 10\ncpu0 1 2")
	if total != 380 || idle != 210 {
		t.Fatalf("%d %d", total, idle)
	}
}
func TestInventory(t *testing.T) {
	r := resources()
	if r.CPUCount < 1 {
		t.Fatal(r)
	}
	if r.MemoryAvailable > r.MemoryTotal {
		t.Fatal(r)
	}
	if r.CPUPercent < 0 || r.CPUPercent > 100 {
		t.Fatal(r)
	}
	if r.Interfaces == nil || r.Drives == nil {
		t.Fatal("nil inventories")
	}
}
