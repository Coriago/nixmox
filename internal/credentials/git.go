package credentials

import (
	"bytes"
	"context"
	"net/url"
	"nixmox/internal/domain"
	"os"
	"path/filepath"
	"strings"
)

func shellQuote(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'" }

// Git receives private runtime filenames, never secret argv, configuration or
// environment values. Authentication is limited to the selected remote; Git
// disables redirects and uses normal TLS/SSH host verification.
func (v *Vault) GitEnvironment(ctx context.Context, ref, remote string) ([]string, func(), error) {
	kind, value, err := v.Secret(ctx, ref)
	if err != nil {
		return nil, nil, err
	}
	defer func() {
		for i := range value {
			value[i] = 0
		}
	}()
	if _, err = Validate(kind, value); err != nil {
		return nil, nil, err
	}
	u, err := url.Parse(remote)
	sshAddress := !strings.Contains(remote, "://") && strings.Contains(remote, "@")
	if err != nil && !sshAddress {
		return nil, nil, unavailable()
	}
	if kind == "git-http-token" && (u == nil || u.Scheme != "https") || kind == "git-ssh-key" && !sshAddress && (u == nil || u.Scheme != "ssh") || kind != "git-http-token" && kind != "git-ssh-key" {
		return nil, nil, domain.Err("CREDENTIAL_KIND", "Git tokens require HTTPS; Git SSH keys require an SSH remote.", 422)
	}
	dir, err := os.MkdirTemp(v.Root, "git-use-")
	if err != nil {
		return nil, nil, unavailable()
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	path := filepath.Join(dir, "secret")
	if kind == "git-http-token" {
		value = bytes.TrimSpace(value)
	}
	if err = os.WriteFile(path, value, 0600); err != nil {
		cleanup()
		return nil, nil, unavailable()
	}
	if kind == "git-ssh-key" {
		command := "ssh -F /dev/null -oBatchMode=yes -oStrictHostKeyChecking=yes -oIdentityAgent=none -oIdentitiesOnly=yes -oConnectTimeout=10 -oUserKnownHostsFile=" + shellQuote(filepath.Join(os.Getenv("HOME"), ".ssh/known_hosts")) + " -i " + shellQuote(path)
		return []string{"GIT_SSH_COMMAND=" + command}, cleanup, nil
	}
	helper := filepath.Join(dir, "askpass")
	// Shell text is constant; the token is read only by the password request.
	script := "#!/bin/sh\ncase \"$1\" in\n  *Username*) printf '%s\\n' x-access-token ;;\n  *Password*) cat -- \"$NIXMOX_GIT_SECRET\" ;;\n  *) exit 1 ;;\nesac\n"
	if err = os.WriteFile(helper, []byte(script), 0700); err != nil {
		cleanup()
		return nil, nil, unavailable()
	}
	return []string{"GIT_ASKPASS=" + helper, "NIXMOX_GIT_SECRET=" + path}, cleanup, nil
}
