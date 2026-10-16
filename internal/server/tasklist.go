package server

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"nixmox/internal/jobs"
	"sort"
	"sync"
	"time"
)

// taskList keeps remote failures visible without hiding healthy hosts' tasks.
func (s *Server) taskList(r *http.Request, all bool) ([]jobs.Job, []string, error) {
	list, err := s.Store.List()
	if err != nil {
		return nil, nil, err
	}
	for i := range list {
		list[i].Preview = nil
	}
	if !all || s.View == nil {
		return list, nil, nil
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	var mu sync.Mutex
	var wg sync.WaitGroup
	slots := make(chan struct{}, 4)
	warnings := []string{}
	for _, peer := range s.View.List() {
		if peer.NodeID == s.Config.Hostname {
			continue
		}
		if !peer.Online {
			mu.Lock()
			warnings = append(warnings, peer.NodeID+": offline; task history unavailable")
			mu.Unlock()
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case slots <- struct{}{}:
				defer func() { <-slots }()
			case <-ctx.Done():
				mu.Lock()
				warnings = append(warnings, peer.NodeID+": task request timed out")
				mu.Unlock()
				return
			}
			var remote []jobs.Job
			err := s.requestPeer(ctx, peer, "GET", "/api/v1/hosts/"+url.PathEscape(peer.NodeID)+"/jobs", nil, &remote)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				warnings = append(warnings, fmt.Sprintf("%s: tasks unavailable (%v)", peer.NodeID, err))
				return
			}
			list = append(list, remote...)
		}()
	}
	wg.Wait()
	sort.Slice(list, func(i, j int) bool { return list[i].CreatedAt.After(list[j].CreatedAt) })
	sort.Strings(warnings)
	return list, warnings, nil
}
