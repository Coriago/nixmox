package portal

import (
	"embed"
	"html/template"
	"io/fs"
	"net/http"
	"nixmox/internal/gitops"
	"nixmox/internal/hostinfo"
	"nixmox/internal/jobs"
)

//go:embed templates/* assets/*
var embeddedFS embed.FS

type Portal struct {
	portalTmpl *template.Template
	jobsTmpl   *template.Template
	gitopsTmpl *template.Template
	fs         fs.FS

	Hostname string
}

// PageData is the unified template context for every page; base.html relies on
// Hostname being present regardless of which page is rendered.
type PageData struct {
	Hostname string
	Hosts    []hostinfo.HostInfo
	Jobs     []jobs.Job
	GitOps   *GitOpsView
}

type GitOpsView struct {
	State            gitops.State
	PollInterval     int
	SSHKeyPath       string
	NetrcPath        string
	AllowConfigWrite bool
}

func New() (*Portal, error) {
	portalTmpl, err := template.ParseFS(embeddedFS, "templates/base.html", "templates/portal.html")
	if err != nil {
		return nil, err
	}
	jobsTmpl, err := template.ParseFS(embeddedFS, "templates/base.html", "templates/jobs.html")
	if err != nil {
		return nil, err
	}
	gitopsTmpl, err := template.ParseFS(embeddedFS, "templates/base.html", "templates/gitops.html")
	if err != nil {
		return nil, err
	}
	return &Portal{
		portalTmpl: portalTmpl,
		jobsTmpl:   jobsTmpl,
		gitopsTmpl: gitopsTmpl,
		fs:         embeddedFS,
	}, nil
}

func (p *Portal) Index(w http.ResponseWriter, hosts []hostinfo.HostInfo) {
	p.portalTmpl.ExecuteTemplate(w, "portal.html", PageData{Hostname: p.Hostname, Hosts: hosts})
}

func (p *Portal) HostsPartial(w http.ResponseWriter, hosts []hostinfo.HostInfo) {
	p.portalTmpl.ExecuteTemplate(w, "hosts_partial", PageData{Hostname: p.Hostname, Hosts: hosts})
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

func (p *Portal) AssetsHandler() http.Handler {
	return http.FileServer(http.FS(p.fs))
}
