package hostinfo

import (
	"os"
	"sync"
	"time"

	agessh "github.com/Mic92/ssh-to-age"
	"golang.org/x/crypto/ssh"
)

const sshHostKey = "/etc/ssh/ssh_host_ed25519_key"

// Cache only public metadata. Retry missing keys and notice rotations without
// parsing a private key on every telemetry sample or browser request.
var ageCache struct {
	sync.Mutex
	at                 time.Time
	recipient, problem string
}

func hostAgeRecipient() (string, string) {
	ageCache.Lock()
	defer ageCache.Unlock()
	if ageCache.at.IsZero() || time.Since(ageCache.at) >= 30*time.Second {
		ageCache.recipient, ageCache.problem = readAgeRecipient(sshHostKey)
		ageCache.at = time.Now()
	}
	return ageCache.recipient, ageCache.problem
}

func readAgeRecipient(path string) (string, string) {
	key, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return "", "SSH Ed25519 host key is missing. Enable OpenSSH with an Ed25519 host key."
	}
	if err != nil {
		return "", "SSH Ed25519 host key cannot be read by Nixmox."
	}
	defer clear(key)
	signer, err := ssh.ParsePrivateKey(key)
	if err != nil {
		return "", "SSH host key is invalid or encrypted; an unencrypted Ed25519 host key is required."
	}
	if signer.PublicKey().Type() != ssh.KeyAlgoED25519 {
		return "", "SSH host key is not Ed25519."
	}
	// Convert only the public half; no age secret is created or persisted.
	recipient, err := agessh.SSHPublicKeyToAge(ssh.MarshalAuthorizedKey(signer.PublicKey()))
	if err != nil {
		return "", "SSH Ed25519 host key could not be converted to an age recipient."
	}
	return *recipient, ""
}
