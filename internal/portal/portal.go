// Package portal renders server-owned HTML; streaming reuses the same fragments.
package portal

import (
	"bytes"
	"embed"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"time"

	"nixmox/internal/cluster"
	"nixmox/internal/gitops"
	"nixmox/internal/hostinfo"
	"nixmox/internal/jobs"
)

//go:embed templates/* assets/*
var embeddedFS embed.FS

type Portal struct {
	pages    map[string]*template.Template
	Hostname string
}
type PageData struct {
	Hostname     string
	Cluster      cluster.ClusterState
	Hosts        []HostRow
	Rollout      *RolloutView
	TaskWarnings []string
	Jobs         []jobs.Job
	GitOps       *GitOpsView
	Detail       *HostDetailView
	Job          *JobDetailView
}
type HostRow struct {
	ConsoleAddress string
	SampledAt      time.Time
	Active         *jobs.Job
	Unavailable    bool
	PulledSHA      string
	RemoteSHA      string
	Host           hostinfo.HostInfo
	Latest         *cluster.Record
	Online         bool
}
type HostDetailView struct {
	NodeID      string
	Host        hostinfo.HostInfo
	Latest      *cluster.Record
	History     []cluster.Record
	RecentJobID string
	RecentLog   []string
	LocalLogs   bool
}
type JobDetailView struct {
	Stream bool
	Job    jobs.Job
	Log    []string
}
type GitOpsView struct {
	State            gitops.State
	PollInterval     int
	SSHKeyPath       string
	NetrcPath        string
	AllowConfigWrite bool
}

func shortSHA(s string) string {
	if len(s) > 8 {
		return s[:8]
	}
	return s
}
func bytesLabel(n uint64) string {
	v := float64(n)
	units := []string{"B", "KiB", "MiB", "GiB", "TiB"}
	i := 0
	for v >= 1024 && i < len(units)-1 {
		v /= 1024
		i++
	}
	return fmt.Sprintf("%.1f %s", v, units[i])
}

var tmplFuncs = template.FuncMap{"shortsha": shortSHA, "bytes": bytesLabel, "jobstate": jobState, "rate": func(n float64) string { return bytesLabel(uint64(n)) + "/s" }}

func parsePage(pages ...string) (*template.Template, error) {
	return template.New("").Funcs(tmplFuncs).ParseFS(embeddedFS, append([]string{"templates/base.html", "templates/resources.html", "templates/task-rows.html", "templates/host-state.html"}, pages...)...)
}
func New() (*Portal, error) {
	p := &Portal{pages: map[string]*template.Template{}}
	for _, name := range []string{"portal", "jobs", "gitops", "host-detail", "job-detail", "setup", "manage"} {
		t, err := parsePage("templates/" + name + ".html")
		if err != nil {
			return nil, err
		}
		p.pages[name] = t
	}
	return p, nil
}
func (p *Portal) render(w http.ResponseWriter, page, fragment string, data PageData) {
	data.Hostname = p.Hostname
	var b bytes.Buffer
	if err := p.pages[page].ExecuteTemplate(&b, fragment, data); err != nil {
		http.Error(w, "render page: "+err.Error(), 500)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(b.Bytes())
}
func (p *Portal) Index(w http.ResponseWriter, rows []HostRow, state cluster.ClusterState) {
	page := "portal"
	if state.Status != "clustered" {
		page = "setup"
	}
	p.render(w, page, page+".html", PageData{Hosts: rows, Cluster: state})
}
func (p *Portal) HostsPartial(w http.ResponseWriter, rows []HostRow) {
	p.render(w, "portal", "hosts_partial", PageData{Hosts: rows})
}
func (p *Portal) SidebarPartial(w http.ResponseWriter, rows []HostRow) {
	p.render(w, "portal", "sidebar_hosts", PageData{Hosts: rows})
}
func (p *Portal) Jobs(w http.ResponseWriter, list []jobs.Job, warnings ...string) {
	p.render(w, "jobs", "jobs.html", PageData{Jobs: list, TaskWarnings: warnings})
}
func (p *Portal) JobsPartial(w http.ResponseWriter, list []jobs.Job, warnings ...string) {
	p.render(w, "jobs", "jobs_partial", PageData{Jobs: list, TaskWarnings: warnings})
}
func (p *Portal) GitOps(w http.ResponseWriter, v GitOpsView) {
	p.render(w, "gitops", "gitops.html", PageData{GitOps: &v})
}
func (p *Portal) GitOpsPartial(w http.ResponseWriter, v GitOpsView) {
	p.render(w, "gitops", "gitops_partial", PageData{GitOps: &v})
}
func (p *Portal) HostDetail(w http.ResponseWriter, v HostDetailView) {
	p.render(w, "host-detail", "host-detail.html", PageData{Detail: &v})
}
func (p *Portal) HostDetailPartial(w http.ResponseWriter, v HostDetailView) {
	p.render(w, "host-detail", "applylog_partial", PageData{Detail: &v})
}
func (p *Portal) HostJobLogPartial(w http.ResponseWriter, v HostDetailView) {
	p.render(w, "host-detail", "hostjoblog_partial", PageData{Detail: &v})
}
func (p *Portal) JobDetail(w http.ResponseWriter, v JobDetailView) {
	p.render(w, "job-detail", "job-detail.html", PageData{Job: &v})
}
func (p *Portal) JobLogPartial(w http.ResponseWriter, v JobDetailView) {
	p.render(w, "job-detail", "joblog_partial", PageData{Job: &v})
}
func (p *Portal) Resources(w http.ResponseWriter, h hostinfo.HostInfo) {
	p.render(w, "host-detail", "resources", PageData{Detail: &HostDetailView{Host: h}})
}
func (p *Portal) AgeRecipient(w http.ResponseWriter, h hostinfo.HostInfo) {
	p.render(w, "host-detail", "age_partial", PageData{Detail: &HostDetailView{Host: h}})
}
func (p *Portal) Manage(w http.ResponseWriter) { p.render(w, "manage", "manage.html", PageData{}) }
func (p *Portal) AssetsHandler() http.Handler {
	assets, _ := fs.Sub(embeddedFS, "assets")
	return http.StripPrefix("/assets/", http.FileServer(http.FS(assets)))
}

func (p *Portal) SyncPartial(w http.ResponseWriter, row HostRow) {
	p.render(w, "host-detail", "sync_partial", PageData{Hosts: []HostRow{row}})
}

func jobState(j jobs.Job) string {
	if j.State == "applied" && j.Kind != "apply" && j.Kind != "rollback" {
		return "completed"
	}
	return j.State
}

type RolloutView struct {
	State, Strategy, Error string
	Hosts, Completed       []string
}

func (p *Portal) RolloutPartial(w http.ResponseWriter, v RolloutView) {
	p.render(w, "manage", "rollout_partial", PageData{Rollout: &v})
}
