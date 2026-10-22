// Package credentials isolates secret bytes from commands and public records.
package credentials

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"filippo.io/age"
	"golang.org/x/crypto/ssh"
	"nixmox/internal/domain"
	"strings"
)

const MaxSecretBytes = 1 << 20

func validKind(kind string) bool {
	switch kind {
	case "git-http-token", "git-ssh-key", "ssh-private-key", "nix-cache-signing-key", "sops-age-key":
		return true
	}
	return false
}
func unavailable() error {
	return domain.Err("CREDENTIAL_UNAVAILABLE", "The selected credential is unavailable or invalid on this node.", 503)
}

func SigningKey(secret []byte) (string, ed25519.PrivateKey, error) {
	value := strings.TrimSpace(string(secret))
	name, encoded, ok := strings.Cut(value, ":")
	if !ok || !domain.SafeID.MatchString(name) {
		return "", nil, unavailable()
	}
	key, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(key) != ed25519.PrivateKeySize || !bytes.Equal(ed25519.NewKeyFromSeed(key[:ed25519.SeedSize]), key) {
		return "", nil, unavailable()
	}
	return name, ed25519.PrivateKey(key), nil
}
func Validate(kind string, value []byte) (domain.CredentialCheck, error) {
	r := domain.CredentialCheck{Kind: kind, Checks: []string{}, Scope: "local_key", Passed: false}
	if len(value) == 0 || len(value) > MaxSecretBytes {
		return r, unavailable()
	}
	challenge := make([]byte, 32)
	if _, err := rand.Read(challenge); err != nil {
		return r, err
	}
	switch kind {
	case "git-http-token":
		if len(value) > 64<<10 || strings.TrimSpace(string(value)) == "" || strings.ContainsAny(strings.TrimSpace(string(value)), "\r\n\x00") {
			return r, unavailable()
		}
		r.Scope = "format_only"
		r.Checks = []string{"bounded_token"}
	case "git-ssh-key", "ssh-private-key":
		key, err := ssh.ParsePrivateKey(value)
		if err != nil {
			return r, unavailable()
		}
		signature, err := key.Sign(rand.Reader, challenge)
		if err != nil || key.PublicKey().Verify(challenge, signature) != nil {
			return r, unavailable()
		}
		r.Checks = []string{"parse", "sign_verify"}
		public := ssh.FingerprintSHA256(key.PublicKey())
		r.PublicKey = &public
	case "nix-cache-signing-key":
		name, key, err := SigningKey(value)
		if err != nil {
			return r, err
		}
		public := key.Public().(ed25519.PublicKey)
		if !ed25519.Verify(public, challenge, ed25519.Sign(key, challenge)) {
			return r, unavailable()
		}
		encoded := name + ":" + base64.StdEncoding.EncodeToString(public)
		r.PublicKey = &encoded
		r.Checks = []string{"parse", "sign_verify"}
	case "sops-age-key":
		identities, err := age.ParseIdentities(bytes.NewReader(value))
		if err != nil || len(identities) != 1 {
			return r, unavailable()
		}
		identity, ok := identities[0].(*age.X25519Identity)
		if !ok {
			return r, unavailable()
		}
		recipient := identity.Recipient().String()
		r.PublicKey = &recipient
		r.Checks = []string{"parse", "derive_recipient"}
	default:
		return r, domain.Err("UNSUPPORTED_CAPABILITY", "Unsupported credential kind.", 422)
	}
	r.Passed = true
	return r, nil
}
