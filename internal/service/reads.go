package service

// Collection metadata binds pagination to each resource's filters and ordering.
// Candidate/deployment/diff entries are retained for the upcoming deployment work.
type Collection struct {
	Path           string
	Filters        []string
	Order          string
	RevisionPinned bool
}

var Collections = []Collection{
	{Path: "hosts", Filters: []string{"membership", "reachability"}, Order: "name,id"},
	{Path: "discoveries", Filters: []string{"kind"}, Order: "id"},
	{Path: "repositories", Order: "name,id"},
	{Path: "configurations", Filters: []string{"repository_id", "ref_id", "commit"}, Order: "name,id", RevisionPinned: true},
	{Path: "candidates", Filters: []string{"repository_id", "configuration"}, Order: "id"},
	{Path: "deployments", Filters: []string{"host_id"}, Order: "-created_at,-id"},
	{Path: "diffs", Filters: []string{"host_id"}, Order: "id"},
	{Path: "tasks", Filters: []string{"host_id", "kind", "state", "since", "until"}, Order: "-created_at,-id"},
	{Path: "credentials", Filters: []string{"kind"}, Order: "name,id"},
	{Path: "cluster/requests", Filters: []string{"state"}, Order: "id"},
}
var NestedReads = []Collection{
	{Path: "cache/locations", Filters: []string{"path"}},
	{Path: "hosts/{id}/generations", Order: "id"},
	{Path: "repositories/{id}/refs", Order: "name,id"},
	{Path: "diffs/{id}/changes", Filters: []string{"kind", "q"}, Order: "id", RevisionPinned: true},
	{Path: "tasks/{id}/activities", Filters: []string{"kind", "state"}, Order: "id"},
}

// Downloads, owner-local log reads and enrollment use explicit route handlers;
// they are not arbitrary filesystem paths handled by this collection catalog.
var DetailReads = []string{
	"hosts/{id}/telemetry", "hosts/{id}/installer",
	"tasks/{id}/progress", "tasks/{id}/logs", "tasks/{id}/logs/download",
	"tasks/{id}/activities/{activity_id}", "tasks/{id}/activities/{activity_id}/logs",
	"tasks/{id}/activities/{activity_id}/logs/download",
	"cache/locations",
	"enrollment/requests/{id}", "membership-status",
}
