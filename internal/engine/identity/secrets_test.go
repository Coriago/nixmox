package identity

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"nixmox/internal/domain"
	"nixmox/internal/engine"
	"nixmox/internal/engine/internal/shared"
)

func TestExpiryIsHolderLocalAndCannotPromote(t *testing.T) {
	ctx := context.Background()
	store := &SecretStore{Root: t.TempDir(), NodeID: "holder"}
	input := engine.SecretIngress{CommandID: "import", HolderID: "holder", Name: "git", Kind: "git-http-token"}
	stage, err := store.Stage(ctx, input, strings.NewReader("private-token"))
	if err != nil {
		t.Fatal(err)
	}
	root, err := store.root()
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	path := "staging/" + shared.Filename(stage.Reference)
	record, err := readStaged(root, path)
	if err != nil {
		t.Fatal(err)
	}
	record.Stage.ExpiresAt = time.Now().Add(-time.Second)
	data, _ := json.Marshal(record)
	if err := writeFile(root, path, data); err != nil {
		t.Fatal(err)
	}
	clear(record.Value)
	clear(data)
	if err := store.Consume(ctx, stage.Reference, "credential"); err == nil {
		t.Fatal("promoted expired bytes")
	}
	if err := store.Expire(ctx); err != nil {
		t.Fatal(err)
	}
	record, err = readStaged(root, path)
	if err != nil || !record.Expired || len(record.Value) != 0 {
		t.Fatal("expiry did not clear bytes", err)
	}
	_, err = store.Stage(ctx, input, strings.NewReader("private-token"))
	var detail *domain.Error
	if !errors.As(err, &detail) || detail.Code != "STAGING_EXPIRED" {
		t.Fatal("expired identity reused", err)
	}
	if err := store.Consume(ctx, stage.Reference, "credential"); err == nil {
		t.Fatal("expired marker promoted")
	}
}
