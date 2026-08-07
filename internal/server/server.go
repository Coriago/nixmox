package server

import (
	"context"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"nixmox/internal/cluster"
	"nixmox/internal/config"
	"nixmox/internal/gitops"
	"nixmox/internal/jobs"
)

type Server struct {
	Mux     *chi.Mux
	srv     *http.Server
	Config  *config.Config
	Store   jobs.Store
	Runner  *jobs.Runner
	Git     GitOpsManager
	Apply   ApplyManager
	Records cluster.RecordStore
	Cluster ClusterAPI
	View    *cluster.View
}

// GitOpsManager is the server's seam to the gitops layer (real Manager in
// production, fakes in tests).
type GitOpsManager interface {
	State() gitops.State
	Configure(url, branch string) error
	Ensure(ctx context.Context) (string, error)
}

// ApplyManager is the server's seam to the apply layer.
type ApplyManager interface {
	Apply(ctx context.Context) (jobs.Job, error)
	Rollback(ctx context.Context) (jobs.Job, error)
}

func (s *Server) SetGitOps(g GitOpsManager) {
	s.Git = g
}

func (s *Server) SetApply(a ApplyManager, r cluster.RecordStore) {
	s.Apply = a
	s.Records = r
}

func (s *Server) SetCluster(c ClusterAPI, v *cluster.View) {
	s.Cluster = c
	s.View = v
}

func New(cfg *config.Config, store jobs.Store, runner *jobs.Runner) *Server {
	mux := chi.NewRouter()

	s := &Server{
		Mux:    mux,
		Config: cfg,
		Store:  store,
		Runner: runner,
		srv: &http.Server{
			Addr:    cfg.HTTPAddr,
			Handler: mux,
		},
	}

	return s
}

func (s *Server) Start(ctx context.Context) error {
	errCh := make(chan error, 1)
	go func() {
		if err := s.srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return s.srv.Shutdown(shutdownCtx)
	case err := <-errCh:
		return err
	}
}
