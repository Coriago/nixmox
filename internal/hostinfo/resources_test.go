package hostinfo

import "testing"

func TestCPUTimes(t *testing.T) {
	total, idle := cpuTimes("cpu 100 20 30 200 10 5 7 8 50 10\ncpu0 1 2")
	if total != 380 || idle != 210 {
		t.Fatalf("%d %d", total, idle)
	}
}
func TestInventory(t *testing.T) {
	r := resources(true)
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

func TestCounterRatesHandleFirstSampleResetAndElapsedTime(t *testing.T) {
	for _, tc := range []struct {
		current, previous uint64
		seconds           float64
		known             bool
		want              float64
		ready             bool
	}{
		{300, 100, 2, true, 100, true}, {0, 300, 2, true, 0, false}, {100, 0, 2, false, 0, false}, {300, 100, 0, true, 0, false},
	} {
		rate, ready := counterRate(tc.current, tc.previous, tc.seconds, tc.known)
		if rate != tc.want || ready != tc.ready {
			t.Fatalf("%+v: %v %v", tc, rate, ready)
		}
	}
}
func TestActivityPreservesInventoryWithoutMutatingSnapshot(t *testing.T) {
	first := resources(true)
	next := resources(true)
	if !first.InventoryAt.Equal(next.InventoryAt) {
		t.Fatal("fast sample refreshed inventory")
	}
	if !next.SampledAt.After(first.SampledAt) {
		t.Fatal("activity did not refresh")
	}
	activity := ActivityOnly(next)
	if activity.CPUModel != "" || len(activity.Filesystems) != 0 {
		t.Fatal("activity repeats inventory")
	}
	merged := MergeActivity(first, activity)
	if merged.CPUModel != first.CPUModel || merged.InventoryAt != first.InventoryAt {
		t.Fatal("lost inventory")
	}
	if len(merged.Interfaces) > 0 && merged.Interfaces[0].MAC != first.Interfaces[0].MAC {
		t.Fatal("lost interface identity")
	}
	if len(activity.Interfaces) > 0 && activity.Interfaces[0].MAC != "" {
		t.Fatal("merge mutated sample")
	}
}
