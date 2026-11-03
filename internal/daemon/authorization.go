package daemon

import (
	"context"
	"encoding/json"
	"sort"

	"nixmox/internal/cluster"
	"nixmox/internal/domain"
)

// refreshAuthorization is also called by the enrollment contact before delivering
// a certificate. The shared MEMBERS record, not an HTTP caller, grants access.
func (r *Runtime) refreshAuthorization(ctx context.Context, store cluster.Store[domain.Member]) error {
	r.authorization.Lock()
	defer r.authorization.Unlock()
	members := []domain.Member{}
	cursor := ""
	for {
		records, next, err := store.List(ctx, cursor, 100)
		if err != nil {
			return err
		}
		for _, record := range records {
			members = append(members, record.Value)
		}
		if next == "" {
			break
		}
		cursor = next
	}
	key := authorizationKey(members)
	if key == r.authorizedMembers {
		return nil
	}
	r.mu.RLock()
	servers, options := r.servers, r.serverOptions
	r.mu.RUnlock()
	for i, s := range servers {
		updated := options[i].Clone()
		if err := cluster.ConfigureAccounts(updated, members); err != nil {
			return err
		}
		if err := s.ReloadOptions(updated); err != nil {
			return err
		}
	}
	r.authorizedMembers = key
	return nil
}

func authorizationKey(members []domain.Member) string {
	type grant struct{ NodeID, Status string }
	grants := make([]grant, 0, len(members))
	for _, member := range members {
		grants = append(grants, grant{member.NodeID, member.Status})
	}
	sort.Slice(grants, func(i, j int) bool { return grants[i].NodeID < grants[j].NodeID })
	data, _ := json.Marshal(grants)
	return string(data)
}
