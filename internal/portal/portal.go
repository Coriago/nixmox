package portal

import (
	"embed"
	"html/template"
	"io/fs"
	"net/http"
	"nixmox/internal/cluster"
	"nixmox/internal/gitops"
	"nixmox/internal/hostinfo"
	"nixmox/internal/jobs"
)

//go:embed templates/* assets/*
var embeddedFS embed.FS

type Portal struct {
	portalTmpl     *template.Template
	jobsTmpl       *template.Template
	gitopsTmpl     *template.Template
	hostDetailTmpl *template.Template
	setupTmpl      *template.Template
	fs             fs.FS

	Hostname string
}

// PageData is the unified template context for every page; base.html relies on
// Hostname being present regardless of which page is rendered.
type PageData struct {
	Hostname string
	Cluster  cluster.ClusterState
	Hosts    []HostRow
	Jobs     []jobs.Job
	GitOps   *GitOpsView
	Detail   *HostDetailView
}

// HostRow pairs a host with its latest apply record (nil = never applied).
type HostRow struct {
	Host   hostinfo.HostInfo
	Latest *cluster.Record
	Online bool
}

type HostDetailView struct {
	NodeID  string
	Host    hostinfo.HostInfo
	Latest  *cluster.Record
	History []cluster.Record
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

var tmplFuncs = template.FuncMap{"shortsha": shortSHA}

func parsePage(pages ...string) (*template.Template, error) {
	files := append([]string{"templates/base.html"}, pages...)
	return template.New("").Funcs(tmplFuncs).ParseFS(embeddedFS, files...)
}

func New() (*Portal, error) {
	portalTmpl, err := parsePage("templates/portal.html")
	if err != nil {
		return nil, err
	}
	jobsTmpl, err := parsePage("templates/jobs.html")
	if err != nil {
		return nil, err
	}
	gitopsTmpl, err := parsePage("templates/gitops.html")
	if err != nil {
		return nil, err
	}
	hostDetailTmpl, err := parsePage("templates/host-detail.html")
	if err != nil {
		return nil, err
	}
	setupTmpl, err := parsePage("templates/setup.html")
	if err != nil {
		return nil, err
	}
	return &Portal{
		portalTmpl:     portalTmpl,
		jobsTmpl:       jobsTmpl,
		gitopsTmpl:     gitopsTmpl,
		hostDetailTmpl: hostDetailTmpl,
		setupTmpl:      setupTmpl,
		fs:             embeddedFS,
	}, nil
}

// Index renders the setup screen while unclustered, the hosts table once clustered.
func (p *Portal) Index(w http.ResponseWriter, rows []HostRow, state cluster.ClusterState) {
	data := PageData{Hostname: p.Hostname, Hosts: rows, Cluster: state}
	if state.Status == "clustered" {
		p.portalTmpl.ExecuteTemplate(w, "portal.html", data)
		return
	}
	p.setupTmpl.ExecuteTemplate(w, "setup.html", data)
}

func (p *Portal) HostsPartial(w http.ResponseWriter, rows []HostRow) {
	p.portalTmpl.ExecuteTemplate(w, "hosts_partial", PageData{Hostname: p.Hostname, Hosts: rows})
}

func (p *Portal) Jobs(w http.ResponseWriter, list []jobs.Job) {
	p.jobsTmpl.ExecuteTemplate(w, "jobs.html", PageData{Hostname: p.Hostname, Jobs: list})
}

func (p *Portal) JobsPartial(w http.ResponseWriter, list []jobs.Job) {
	p.jobsTmpl.ExecuteTemplate(w, "jobs_partial", PageData{Hostname: p.Hostname, Jobs: list})
}

func (p *Portal) GitOps(w http.ResponseWriter, view GitOpsView) {
	p.gitopsTmpl.ExecuteTemplate(w, "gitops.html", PageData{Hostname: p.Hostname, GitOps: &view})
}

func (p *Portal) GitOpsPartial(w http.ResponseWriter, view GitOpsView) {
	p.gitopsTmpl.ExecuteTemplate(w, "gitops_partial", PageData{Hostname: p.Hostname, GitOps: &view})
}

func (p *Portal) HostDetail(w http.ResponseWriter, view HostDetailView) {
	p.hostDetailTmpl.ExecuteTemplate(w, "host-detail.html", PageData{Hostname: p.Hostname, Detail: &view})
}

func (p *Portal) HostDetailPartial(w http.ResponseWriter, view HostDetailView) {
	p.hostDetailTmpl.ExecuteTemplate(w, "applylog_partial", PageData{Hostname: p.Hostname, Detail: &view})
}

func (p *Portal) AssetsHandler() http.Handler {
	return http.FileServer(http.FS(p.fs))
}
