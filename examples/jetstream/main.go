// Executable API walkthrough retained while the architecture is restubbed.
// Interface functions currently return ErrUnsupported; no cluster starts yet.
package main

import (
	"context"
	"log"
)

func main() {
	walkthrough(context.Background())
}

func walkthrough(ctx context.Context) {
	a := &Daemon{HostID: "A"}
	b := &Daemon{HostID: "B"}
	c := &Daemon{HostID: "C"}
	defer a.Close(ctx)
	defer b.Close(ctx)
	defer c.Close(ctx)

	if err := a.CreateCluster(ctx); err != nil {
		log.Printf("create cluster: %v", err)
		return
	}
	log.Print("cluster created: primary + bootstrap, MEMBERS + TASKS (2 replicas)")
	if err := b.Enroll(ctx, a); err != nil {
		log.Printf("enroll B: %v", err)
		return
	}
	if err := c.Enroll(ctx, a); err != nil {
		log.Printf("enroll C: %v", err)
		return
	}

	if err := a.Promote(ctx, b.HostID); err != nil {
		log.Printf("promote B: %v", err)
		return
	}
	if err := b.Promote(ctx, c.HostID); err != nil {
		log.Printf("promote C: %v", err)
		return
	}
	log.Print("B and C promoted; replication is 3 and bootstrap retired")

	// A sends the request; C owns and manages its demotion task.
	if err := a.Demote(ctx, c.HostID); err != nil {
		log.Printf("demote C: %v", err)
		return
	}
	log.Print("C demoted to full; replacement bootstrap ready")

	if err := a.Demote(ctx, b.HostID); err != nil {
		log.Printf("demote B: %v", err)
		return
	}
	log.Print("B demoted; A primary and bootstrap retain two replicas")
	if err := a.Promote(ctx, b.HostID); err != nil {
		log.Printf("re-promote B: %v", err)
		return
	}
	if err := b.Demote(ctx, a.HostID); err != nil {
		log.Printf("demote A / transfer bootstrap: %v", err)
		return
	}
	log.Print("A demoted; bootstrap transferred to B")
	if err := b.Demote(ctx, b.HostID); err == nil {
		log.Print("ERROR: last full-stable demotion should fail")
		return
	}
	log.Print("last full-stable demotion correctly rejected")

	c.Stop(ctx, false)
	b.Stop(ctx, true)
	a.Stop(ctx, false)
}
