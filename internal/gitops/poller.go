package gitops

import (
	"context"
	"log"
)

type Poller struct {
	mgr   *Manager
	onNew func(newSHA string)
}

func NewPoller(mgr *Manager, onNew func(newSHA string)) *Poller {
	return &Poller{mgr: mgr, onNew: onNew}
}

func (p *Poller) Tick(ctx context.Context) {
	changed, sha, err := p.mgr.CheckRemote(ctx)
	if err != nil {
		log.Printf("gitops poll: %v", err)
		return
	}
	if changed && p.onNew != nil {
		p.onNew(sha)
	}
}
