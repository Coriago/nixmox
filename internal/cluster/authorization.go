package cluster

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/nats-io/nats-server/v2/server"
	"nixmox/internal/domain"
)

// SystemIdentity names a separate certificate-mapped management principal.
// Colons are forbidden in member IDs, keeping this namespace disjoint.
func SystemIdentity(nodeID string) string { return "system:" + nodeID }

// ConfigureAccounts builds independent server accounts from authoritative
// membership. A pending certificate grants reads and its own telemetry, never
// membership writes or JetStream administration. Enrollment is contact-owned.
func ConfigureAccounts(options *server.Options, members []domain.Member) error {
	type permissions struct {
		Publish        []string       `json:"publish"`
		Subscribe      []string       `json:"subscribe"`
		AllowResponses map[string]any `json:"allow_responses,omitempty"`
	}
	type user struct {
		Name        string       `json:"user"`
		Permissions *permissions `json:"permissions,omitempty"`
	}
	users := []user{}
	systemUsers := []user{}
	seen := map[string]bool{}
	for _, member := range members {
		if member.NodeID == "" || strings.ContainsAny(member.NodeID, " :.*><\t\r\n") || seen[member.NodeID] {
			return errors.New("invalid or duplicate member identity")
		}
		seen[member.NodeID] = true
		u := user{Name: "CN=" + member.NodeID}
		switch member.Status {
		case "full", "full-stable":
			systemID := SystemIdentity(member.NodeID)
			systemUsers = append(systemUsers, user{Name: "CN=" + systemID, Permissions: &permissions{
				Publish:   []string{server.JSApiRemoveServer},
				Subscribe: []string{"_INBOX." + systemID + ".>"},
			}})
			u.Permissions = &permissions{
				Publish:        []string{"$JS.API.>", "$JS.ACK.>", "$KV.>", "logs.task.>", "logs.host." + member.NodeID, "metrics.host." + member.NodeID, "host.*.observe", "host.*.membership", "membership.*.request." + member.NodeID},
				Subscribe:      []string{"_INBOX." + member.NodeID + ".>", "metrics.host.>", "host." + member.NodeID + ".observe", "host." + member.NodeID + ".membership", "membership." + member.NodeID + ".request.*"},
				AllowResponses: map[string]any{"max": 1, "expires": "1m"},
			}
		case "pending":
			u.Permissions = &permissions{
				Publish:        []string{"$JS.API.INFO", "$JS.ACK.>", "metrics.host." + member.NodeID, "logs.task." + member.NodeID + ".>", "$KV.TASKS." + member.NodeID + ".>", "$KV.OUTPUTS." + member.NodeID + ".>", "$KV.LOCKS.host." + member.NodeID, "membership.*.request." + member.NodeID, "$JS.API.STREAM.INFO.KV_CREDENTIALS", "$JS.API.STREAM.INFO.KV_RECIPIENTS"},
				Subscribe:      []string{"_INBOX." + member.NodeID + ".>", "metrics.host.>", "host." + member.NodeID + ".observe", "host." + member.NodeID + ".membership"},
				AllowResponses: map[string]any{"max": 1, "expires": "1m"},
			}
			for _, bucket := range []string{"CLUSTER", "MEMBERS", "DISCOVERY", "TASKS", "OUTPUTS", "LOCKS", "REPOS", "CONFIGURATIONS", "RECIPES", "INSTALLERS", "ENROLLMENT"} {
				stream := "KV_" + bucket
				u.Permissions.Publish = append(u.Permissions.Publish,
					"$JS.API.STREAM.INFO."+stream,
					"$JS.API.DIRECT.GET."+stream+".>",
					"$JS.API.STREAM.MSG.GET."+stream,
					"$JS.API.CONSUMER.CREATE."+stream+".>",
					"$JS.API.CONSUMER.DELETE."+stream+".*")
			}
		default:
			// Removed/unjoined identities are not authorized, even with a valid cert.
			continue
		}
		users = append(users, u)
	}
	if len(users) == 0 {
		return errors.New("at least one authorized member required")
	}
	var data bytes.Buffer
	encoder := json.NewEncoder(&data)
	encoder.SetEscapeHTML(false) // NATS config strings do not accept JSON Unicode escapes.
	accounts := map[string]any{
		"APP": map[string]any{"jetstream": "enabled", "users": users},
		"SYS": map[string]any{"users": systemUsers},
	}
	if err := encoder.Encode(accounts); err != nil {
		return err
	}
	var parsed server.Options
	if err := parsed.ProcessConfigString(fmt.Sprintf(`accounts %s
system_account: SYS`, data.String())); err != nil {
		return err
	}
	options.Accounts, options.Users, options.SystemAccount = parsed.Accounts, parsed.Users, parsed.SystemAccount
	return nil
}
