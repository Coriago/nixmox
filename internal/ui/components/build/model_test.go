package build_test

import (
	"fmt"
	"nixmox/internal/ui/components/build"
)

func ExampleModel() {
	// --- Test setup ---
	// ------------------
	var monitor build.Model

	// --- Usage ---
	// -------------
	monitor.ApplyLine(`@nix {"action":"start","id":1,"type":104}`)
	monitor.ApplyLine(`@nix {"action":"start","id":2,"parent":1,"type":105,"fields":["/nix/store/00000000000000000000000000000000-hello.drv"]}`)
	monitor.ApplyLine(`@nix {"action":"result","id":1,"type":105,"fields":[0,1,1,0]}`)
	monitor.ApplyLine(`@nix {"action":"result","id":2,"type":104,"fields":["buildPhase"]}`)
	monitor.ApplyLine(`@nix {"action":"result","id":2,"type":101,"fields":["compiling hello"]}`)
	fmt.Println(monitor.Summary())
	for _, row := range monitor.Rows() {
		fmt.Println(row.Kind(), row.Name, row.Progress())
	}
	monitor.ApplyLine(`@nix {"action":"stop","id":2}`)
	fmt.Println(monitor.Rows()[1].State())
	fmt.Println("task outcome known:", monitor.Status != "")
	monitor.Status = "succeeded"
	fmt.Println(monitor.Log.Lines[0])
	fmt.Println(monitor.Summary())

	// --- Assertions ---
	// ------------------

	// Output:
	// 1 active builds · 0 active downloads
	// Builds  0 / 1
	// Build hello Total unknown
	// buildPhase · stopped
	// task outcome known: false
	// compiling hello
	// Task succeeded · activity stops alone do not imply successful builds
}
