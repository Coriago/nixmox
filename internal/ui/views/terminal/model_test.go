package terminal

import (
	tea "charm.land/bubbletea/v2"
	"fmt"
	"nixmox/internal/domain"
	"nixmox/internal/ui/views"
)

func ExampleModel() {
	// --- Test setup ---
	// ------------------
	core := views.New()
	core.Apply(views.Update{Snapshot: &views.Snapshot{Health: domain.Health{Status: "online"}}})
	core.Dispatch(views.Action{Kind: "recipe-create"})
	terminal := New(core)

	// --- Usage ---
	// -------------
	terminal.activate(views.Action{Kind: "form-field", ID: "json"})
	terminal.formInput.Text.SetValue("{\n  \"name\": \"Example\"\n}")
	terminal.Update(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	fmt.Println("editor closed:", !terminal.formInput.Active)
	fmt.Println(core.Form.Fields[0].Value)

	// --- Assertions ---
	// ------------------

	// Output:
	// editor closed: true
	// {
	//   "name": "Example"
	// }
}
