package service

// Collection metadata is shared by API routing, pagination and console adapters.
// Revision-pinned reads reject a changed source instead of mixing pages.
type Collection struct {
	Path           string
	Filters        []string
	Order          string
	RevisionPinned bool
}

var Collections = []Collection{
	{Path: "hosts", Filters: []string{"membership", "reachability"}, Order: "name,id"},
	{Path: "clusters", Order: "name,id"},
	{Path: "discoveries", Filters: []string{"kind"}, Order: "id"},
	{Path: "repositories", Order: "name,id"},
	{Path: "configurations", Filters: []string{"repository_id", "ref_id", "commit"}, Order: "name,id", RevisionPinned: true},
	{Path: "drafts", Filters: []string{"repository_id"}, Order: "id"},
	{Path: "recipes", Filters: []string{"repository_id", "configuration_id"}, Order: "id"},
	{Path: "preparations", Filters: []string{"host_id"}, Order: "id"},
	{Path: "media", Filters: []string{"profile_id"}, Order: "id"},
	{Path: "media/plans", Filters: []string{"writer_id"}, Order: "id"},
	{Path: "candidates", Filters: []string{"repository_id", "configuration"}, Order: "id"},
	{Path: "deployments", Filters: []string{"host_id"}, Order: "-created_at,-id"},
	{Path: "diffs", Filters: []string{"host_id"}, Order: "id"},
	{Path: "tasks", Filters: []string{"host_id", "kind", "state", "since", "until"}, Order: "-created_at,-id"},
	{Path: "credentials", Filters: []string{"kind"}, Order: "name,id"},
	{Path: "observations", Filters: []string{"host_id"}, Order: "id"},
	{Path: "publications", Filters: []string{"repository_id"}, Order: "-created_at,-id"},
	{Path: "cluster/requests", Filters: []string{"state"}, Order: "id"},
	{Path: "gc-plans", Filters: []string{"repository_id"}, Order: "id"},
	{Path: "diagnostics", Filters: []string{"host_id"}, Order: "-created_at,-id"},
}
var Singletons = []string{
	"settings", "identity", "cluster/status", "cache/status",
	"network/status", "network/wifi/networks", "media/profiles",
}
var NestedReads = []Collection{
	{Path: "recipes/{id}/versions", Order: "-version", RevisionPinned: true},
	{Path: "cache/locations", Filters: []string{"path"}},
	{Path: "hosts/{id}/generations", Order: "id"},
	{Path: "hosts/{id}/disks", Order: "id"},
	{Path: "repositories/{id}/refs", Order: "name,id"},
	{Path: "repositories/{id}/templates", Filters: []string{"ref_id", "commit"}, Order: "name,id", RevisionPinned: true},
	{Path: "drafts/{id}/files", Filters: []string{"version"}, Order: "path", RevisionPinned: true},
	{Path: "diffs/{id}/changes", Filters: []string{"kind", "q"}, Order: "id", RevisionPinned: true},
	{Path: "tasks/{id}/activities", Filters: []string{"kind", "state"}, Order: "id"},
}

// Downloads, owner-local log reads and enrollment use explicit route handlers;
// they are not arbitrary filesystem paths handled by this collection catalog.
var DetailReads = []string{
	"recipes/{id}/version/{version_id}",
	"hosts/{id}/telemetry", "hosts/{id}/installer",
	"drafts/{id}/diff", "drafts/{id}/files/content",
	"preparations/{id}/diff", "repositories/{id}/diff",
	"tasks/{id}/progress", "tasks/{id}/logs", "tasks/{id}/logs/download",
	"tasks/{id}/activities/{activity_id}", "tasks/{id}/activities/{activity_id}/logs",
	"tasks/{id}/activities/{activity_id}/logs/download",
	"media/{id}/download", "diagnostics/{id}/download", "cache/locations",
	"enrollment/requests/{id}", "membership-status",
}
