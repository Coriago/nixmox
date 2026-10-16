package server

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"nixmox/internal/jobs"
	"nixmox/internal/media"
)

func (s *Server) mountMedia() {
	if s.Runner != nil {
		s.Runner.Register("installer-build", s.buildInstaller)
		s.Runner.Register("installer-write", s.writeInstaller)
	}
	s.Mux.Get("/api/v1/media", s.mediaStatus)
	s.Mux.Post("/api/v1/media/build", s.prepareMedia)
	s.Mux.Post("/api/v1/media/write", s.writeMedia)
}
func (s *Server) buildInstaller(ctx context.Context, j *jobs.Job, out io.Writer) error {
	if !s.Config.EnableMedia || s.Config.InstallerFlake == "" {
		return fmt.Errorf("USB installer writing is not configured")
	}
	j.Phase = "Building x86_64 BIOS/UEFI hybrid installer"
	if err := s.Store.Update(*j); err != nil {
		return err
	}
	key := ""
	if len(j.Args) > 0 {
		key = j.Args[0]
	}
	key, err := media.NormalizeKey(key)
	if err != nil {
		return err
	}
	if err := jobs.RunCommand(ctx, j, out, "nix", media.BuildArgs(s.Config.InstallerFlake, key, s.Config.DataDirPath("installer"))...); err != nil {
		return err
	}
	image, err := media.Image(s.Config.DataDirPath("installer"))
	if err != nil {
		return err
	}
	j.Phase = "Installer ready to write"
	fmt.Fprintln(out, "Prepared", image)
	return nil
}

func (s *Server) writeInstaller(ctx context.Context, j *jobs.Job, out io.Writer) error {
	if !s.Config.EnableMedia || len(j.Args) != 3 {
		return fmt.Errorf("USB writing disabled or invalid request")
	}
	fmt.Fprintf(out, "Writing %s to %s; all existing drive contents will be lost.\n", j.Args[0], j.Args[1])
	lastPhase, lastPercent := "", -1
	return media.Write(ctx, j.Args[0], j.Args[1], j.Args[2], func(phase string, done, total int) error {
		percent := 0
		if total > 0 {
			percent = done * 100 / total
		}
		if phase == lastPhase && percent == lastPercent {
			return nil
		}
		lastPhase, lastPercent = phase, percent
		j.Phase, j.Progress, j.Total = phase, done, total
		fmt.Fprintf(out, "%s: %d / %d MiB\n", phase, done, total)
		return s.Store.Update(*j)
	})
}

func (s *Server) mediaStatus(w http.ResponseWriter, r *http.Request) {
	if !s.Config.EnableMedia {
		http.Error(w, "Enable services.nixmox.enableMedia to prepare USB installers on this node", 403)
		return
	}
	devices, err := media.List(r.Context())
	if err != nil {
		http.Error(w, err.Error(), 503)
		return
	}
	image, imageErr := s.preparedImage()
	imageError := ""
	if imageErr != nil {
		imageError = imageErr.Error()
	}
	active, err := s.activeMediaJob()
	if err != nil {
		http.Error(w, err.Error(), 503)
		return
	}
	respond(w, 200, map[string]any{"image_error": imageError, "active": active, "devices": devices, "image": image, "flake": s.Config.InstallerFlake, "host": s.Config.Hostname})
}

func (s *Server) prepareMedia(w http.ResponseWriter, r *http.Request) {
	s.mediaMu.Lock()
	defer s.mediaMu.Unlock()
	if !s.mediaIdle(w) {
		return
	}
	if !s.Config.EnableMedia || s.Config.InstallerFlake == "" {
		http.Error(w, "USB installer building is not configured", 403)
		return
	}
	var req struct {
		SSHKey string `json:"ssh_key"`
	}
	if err := decode(r, &req); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	key, err := media.NormalizeKey(req.SSHKey)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	j, err := s.Runner.Submit(jobs.Job{Kind: "installer-build", Host: s.Config.Hostname, Args: []string{key}})
	if err != nil {
		http.Error(w, err.Error(), 409)
		return
	}
	respond(w, 202, j)
}

func (s *Server) writeMedia(w http.ResponseWriter, r *http.Request) {
	s.mediaMu.Lock()
	defer s.mediaMu.Unlock()
	if !s.mediaIdle(w) {
		return
	}
	if !s.Config.EnableMedia {
		http.Error(w, "USB writing disabled", 403)
		return
	}
	var req struct {
		Device   string `json:"device"`
		Identity string `json:"identity"`
		Confirm  string `json:"confirm"`
	}
	if err := decode(r, &req); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if req.Device == "" || req.Confirm != req.Device {
		http.Error(w, "type the exact device path to confirm erasure", 400)
		return
	}
	devices, err := media.List(r.Context())
	if err != nil {
		http.Error(w, err.Error(), 503)
		return
	}
	if _, err := media.Select(devices, req.Device, req.Identity); err != nil {
		http.Error(w, err.Error(), 409)
		return
	}
	image, err := s.preparedImage()
	if err != nil {
		http.Error(w, err.Error(), 409)
		return
	}
	j, err := s.Runner.Submit(jobs.Job{Kind: "installer-write", Host: s.Config.Hostname, Args: []string{image, req.Device, req.Identity}})
	if err != nil {
		http.Error(w, err.Error(), 409)
		return
	}
	respond(w, 202, j)
}

func (s *Server) activeMediaJob() (*jobs.Job, error) {
	list, err := s.Store.List()
	if err != nil {
		return nil, err
	}
	for _, j := range list {
		if (j.Kind == "installer-build" || j.Kind == "installer-write") && (j.State == "pending" || j.State == "building" || j.State == "applying") {
			return &j, nil
		}
	}
	return nil, nil
}
func (s *Server) mediaIdle(w http.ResponseWriter) bool {
	active, err := s.activeMediaJob()
	if err != nil {
		http.Error(w, err.Error(), 503)
		return false
	}
	if active != nil {
		http.Error(w, "Installer task already active: "+active.ID, 409)
		return false
	}
	return true
}

// A failed replacement build must not silently authorize the previous image.
func (s *Server) preparedImage() (string, error) {
	list, err := s.Store.List()
	if err != nil {
		return "", err
	}
	for _, j := range list {
		if j.Kind == "installer-build" {
			if j.State != "applied" {
				return "", fmt.Errorf("Latest installer build is %s; inspect task %s before preparing again", j.State, j.ID)
			}
			break
		}
	}
	return media.Image(s.Config.DataDirPath("installer"))
}
