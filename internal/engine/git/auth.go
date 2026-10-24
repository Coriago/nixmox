package git

import (
	"context"
	"errors"
	"net/url"
	"strings"

	gogit "github.com/go-git/go-git/v6"
	githttp "github.com/go-git/go-git/v6/plumbing/transport/http"
	gitssh "github.com/go-git/go-git/v6/plumbing/transport/ssh"
	"github.com/go-git/go-git/v6/storage/memory"
	"golang.org/x/crypto/ssh"

	"nixmox/internal/domain"
)

// ManagedAuthentication never consults an SSH agent, machine private key, user
// config or TOFU database. HostKey is the Git server's public key, not a client key.
func ManagedAuthentication(remote domain.Remote, kind string, value []byte) (Authentication, error) {
	u, err := url.Parse(remote.URL)
	if err != nil {
		return Authentication{}, errors.New("invalid Git remote")
	}
	username := remote.Username
	if username == "" {
		username = "git"
	}
	switch kind {
	case "git-http-token":
		if u.Scheme != "https" || len(value) == 0 {
			return Authentication{}, errors.New("Git token requires HTTPS")
		}
		return Authentication{HTTP: &githttp.BasicAuth{Username: username, Password: strings.TrimSpace(string(value))}}, nil
	case "git-ssh-key":
		if u.Scheme != "ssh" {
			return Authentication{}, errors.New("Git SSH key requires SSH transport")
		}
		if u.User != nil {
			username = u.User.Username()
		}
		signer, err := ssh.ParsePrivateKey(value)
		if err != nil {
			return Authentication{}, errors.New("Git SSH private key is invalid or encrypted")
		}
		key, _, _, rest, err := ssh.ParseAuthorizedKey([]byte(remote.HostKey))
		if err != nil || len(strings.TrimSpace(string(rest))) != 0 {
			return Authentication{}, errors.New("one verified Git server public host key is required")
		}
		keys := &gitssh.PublicKeys{User: username, Signer: signer}
		keys.HostKeyCallback = ssh.FixedHostKey(key)
		return Authentication{SSH: keys}, nil
	default:
		return Authentication{}, errors.New("credential kind cannot authenticate Git")
	}
}

// CheckRead lists refs without fetching, pushing, or changing accepted metadata.
func (g *Engine) CheckRead(ctx context.Context, repository domain.Repository) error {
	remote, auth, err := g.remote(ctx, repository, memory.NewStorage())
	if err != nil {
		return err
	}
	_, err = remote.ListContext(ctx, &gogit.ListOptions{ClientOptions: auth.options()})
	if err != nil {
		return &domain.Error{Code: "CREDENTIAL_UNAVAILABLE", Message: "Git read access check failed; no write permission was tested"}
	}
	return nil
}
