package presentation

import (
	"fmt"
	"nixmox/internal/domain"
)

type Shared struct{}

func (Shared) Form(schema domain.Template, values domain.Values, issues []domain.FieldError) Form {
	effective, _ := valuesWithDefaults(schema, values)
	form := Form{Fields: []Field{}, Actions: []Action{}, Warnings: []string{}}
	for _, field := range schema.Fields {
		view := Field{Definition: field, Value: effective[field.Name]}
		for _, issue := range issues {
			if issue.Field == field.Name {
				view.Errors = append(view.Errors, issue)
			}
		}
		form.Fields = append(form.Fields, view)
	}
	return form
}
func (Shared) HostActions(host domain.Host, caps domain.Capabilities) []Action {
	action := func(id, label string, enabled bool, reason string) Action {
		a := Action{ID: id, Label: label, Enabled: enabled}
		if !enabled {
			a.Reason = &reason
		}
		return a
	}
	online := host.Reachability == domain.Online
	return []Action{
		action("deployment.apply", "Apply latest", online && host.Environment == domain.Installed && caps.Build && host.Configuration != nil, "Requires a reachable installed NixOS host, assigned configuration and build capability"),
		action("deployment.install", "Install", online && caps.Install && host.Environment == domain.Installer, "Requires verified installer entry and install capability"),
		action("host.reboot", "Reboot", online && caps.Power, "Requires reachable host and power capability"),
	}
}
func (Shared) TaskProgress(p domain.TaskProgress) Progress {
	result := Progress{Phase: p.Phase, Freshness: p.Freshness, Completed: p.Downloads.BytesReceived, Total: p.Downloads.BytesTotal, Label: "Download total unavailable"}
	if result.Completed != nil && result.Total != nil && *result.Total > 0 {
		ratio := min(float64(*result.Completed)/float64(*result.Total), 1.0)
		result.Ratio = &ratio
		result.Label = fmt.Sprintf("%.0f%% · %d / %d bytes known", ratio*100, *result.Completed, *result.Total)
	}
	if p.Freshness != domain.Live {
		result.Label += " · " + string(p.Freshness)
	}
	return result
}
func (Shared) PackageSummary(s domain.DiffSummary) string {
	return fmt.Sprintf("%d added · %d removed · %d version changed · %d output changed · %d ambiguous · %d unchanged", s.Added, s.Removed, s.VersionChanged, s.OutputChanged, s.Ambiguous, s.Unchanged)
}
