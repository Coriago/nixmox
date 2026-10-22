// Package presentation is shared by native interfaces and browser preview WASM.
// It must not import OS, transport, secret-storage or terminal libraries.
package presentation

import "nixmox/internal/domain"

type Action struct {
	ID, Label string
	Enabled   bool
	Reason    *string
}
type Field struct {
	Definition domain.Field
	Value      any
	Errors     []domain.FieldError
}
type Form struct {
	Fields   []Field
	Actions  []Action
	Warnings []string
}
type Progress struct {
	Phase            string
	Freshness        domain.Freshness
	Completed, Total *uint64
	Ratio            *float64
	Label            string
}
type PreviewRequest struct {
	Revision     uint64               `json:"revision"` // Client sequence; discard obsolete results.
	Template     domain.Template      `json:"template"`
	SourceCommit domain.Commit        `json:"source_commit"`
	Templates    map[string]string    `json:"templates"` // Pinned content, no arbitrary fetch.
	Values       domain.Values        `json:"values"`
	Files        []domain.DraftBuffer `json:"files"`
}
type PreviewResult struct {
	Revision uint64               `json:"revision"`
	Files    []domain.DraftBuffer `json:"files"`
	Errors   []domain.FieldError  `json:"errors"`
}
type Editor struct {
	DraftID          *domain.ID
	ExpectedVersions domain.Versions
	Revision         uint64
	Values           domain.Values
	Files            []domain.DraftBuffer
	SelectedPath     string
	Cursor, Scroll   int
	Dirty            bool
}

// Rendering is deterministic and bounded. It preserves manual edits separately
// from regenerated baselines and cannot evaluate Nix or authorize an effect.
type Renderer interface {
	Validate(domain.Template, domain.Values) []domain.FieldError
	Preview(PreviewRequest) PreviewResult
}
type Presentation interface {
	Form(domain.Template, domain.Values, []domain.FieldError) Form
	HostActions(domain.Host, domain.Capabilities) []Action
	TaskProgress(domain.TaskProgress) Progress
	PackageSummary(domain.DiffSummary) string
}
