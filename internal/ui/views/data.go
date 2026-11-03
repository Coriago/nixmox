package views

import (
	"encoding/json"

	"nixmox/internal/domain"
	"nixmox/internal/ui/components/build"
	logs "nixmox/internal/ui/components/log"
)

// Document is a read-only UI projection. Raw retains versioned API fields for
// inspection/editing without importing server services into the WASM binary.
type Document struct {
	ID      string          `json:"id"`
	Name    string          `json:"name"`
	Version uint64          `json:"version"`
	Raw     json.RawMessage `json:"-"`
}

func (d *Document) UnmarshalJSON(data []byte) error {
	type metadata Document
	var value metadata
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	*d = Document(value)
	d.Raw = append(json.RawMessage(nil), data...)
	return nil
}
func (d Document) Property(name string) json.RawMessage {
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(d.Raw, &fields)
	return fields[name]
}

func (d Document) Has(name string) bool {
	value := d.Property(name)
	return len(value) > 0 && string(value) != "null"
}

type Host struct {
	Source                        domain.Member
	ID, Name, Address, Membership string
}

type Task struct {
	Source                  domain.Task
	ID, Name, HostID, State string
	Log                     logs.Model
	Build                   build.Model
	LastLog                 uint64
}
