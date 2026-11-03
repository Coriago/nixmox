package views

import (
	"errors"
	"fmt"
	"time"

	"nixmox/internal/domain"
)

func ExampleModel() {
	// --- Test setup ---
	// ------------------
	model := New()

	// --- Usage ---
	// -------------
	model.Advance(time.Second)
	fmt.Println(model.View().Connection)
	fmt.Println("invented records:", len(model.Hosts)+len(model.Tasks))
	model.Apply(Update{Snapshot: &Snapshot{
		Health: domain.Health{NodeID: "node", Status: "online"},
	}})
	model.Dispatch(Action{Kind: "cluster-create"})
	model.SetFormValue("cluster", "name", "Example cluster")
	model.Dispatch(Action{Kind: "source-form-save"})
	request := model.TakeCommand()
	fmt.Println(request.Method, request.Path, string(request.Body))
	fmt.Println("cluster before response:", model.Cluster.ID == "")
	model.Apply(Update{Submitted: true, Err: errors.New("invalid cluster name")})
	fmt.Println("rejected request retains form:", model.Form != nil)

	// --- Assertions ---
	// ------------------

	// Output:
	// Connecting…
	// invented records: 0
	// POST /cluster {"name":"Example cluster"}
	// cluster before response: true
	// rejected request retains form: true
}
