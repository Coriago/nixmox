package broker

import (
	"errors"
	"slices"
)

// AllowLeaves adds grants at the accepting hub. Removing privileges requires a
// fenced broker restart, which also closes handshakes already past authentication.
func (b *Broker) AllowLeaves(leaves map[string]LeafGrant) error {
	next := make(map[string]LeafGrant, len(leaves))
	for key, g := range leaves {
		g.Tasks = append([]string(nil), g.Tasks...)
		g.Acknowledgments = append([]string(nil), g.Acknowledgments...)
		next[key] = g
	}
	c := b.config
	c.Leaves = next
	if err := validateGrants(c); err != nil {
		return err
	}
	b.auth.mu.Lock()
	changed := false
	for key, old := range b.auth.config.Leaves {
		grant, ok := next[key]
		if !ok || grant.NodeID != old.NodeID {
			b.auth.mu.Unlock()
			return errors.New("leaf revocation requires a fenced broker restart")
		}
		for _, task := range old.Tasks {
			if !slices.Contains(grant.Tasks, task) {
				b.auth.mu.Unlock()
				return errors.New("task grant revocation requires a fenced broker restart")
			}
		}
		for _, ack := range old.Acknowledgments {
			if !slices.Contains(grant.Acknowledgments, ack) {
				b.auth.mu.Unlock()
				return errors.New("acknowledgment revocation requires a fenced broker restart")
			}
		}
		if !slices.Equal(old.Tasks, grant.Tasks) || !slices.Equal(old.Acknowledgments, grant.Acknowledgments) {
			changed = true
		}
	}
	b.auth.config.Leaves = next
	b.auth.mu.Unlock()
	if !changed {
		return nil
	}
	connections, err := b.Server.Leafz(nil)
	if err != nil {
		return err
	}
	for _, leaf := range connections.Leafs {
		if err := b.Server.DisconnectClientByID(leaf.ID); err != nil && b.Server.GetLeafNode(leaf.ID) != nil {
			return errors.New("leaf permissions could not be refreshed")
		}
	}
	return nil
}
