//go:build !js

package browser

import (
	button "nixmox/internal/ui/components/button/browser"
	confirmation "nixmox/internal/ui/components/confirmation/browser"
	logs "nixmox/internal/ui/components/log/browser"
	progress "nixmox/internal/ui/components/progress/browser"
	search "nixmox/internal/ui/components/search/browser"
	section "nixmox/internal/ui/components/section/browser"
	table "nixmox/internal/ui/components/table/browser"
	telemetry "nixmox/internal/ui/components/telemetry/browser"
	theme "nixmox/internal/ui/components/theme/browser"
)

// Component styles travel with their adapters; shell rules only place views.
var styles = theme.CSS + button.CSS + section.CSS + table.CSS + progress.CSS + telemetry.CSS + confirmation.CSS + logs.CSS + search.CSS + stylesheet
