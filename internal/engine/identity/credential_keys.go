package identity

import (
	"bytes"
	"crypto/rand"
	"io"
	"os"
	"strings"
	"unicode"

	"filippo.io/age"
	"github.com/nix-community/go-nix/pkg/narinfo/signature"
	"golang.org/x/crypto/ssh"

	"nixmox/internal/domain"
)

// ReadCredentialFile accepts runtime-only files, never ambient SSH agents or keys.
func ReadCredentialFile(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, credentialUnavailable()
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return nil, credentialUnavailable()
	}
	value, err := secretBytes(file)
	if err != nil {
		return nil, credentialUnavailable()
	}
	return value, nil
}

func credentialUnavailable() error {
	return &domain.Error{Code: "CREDENTIAL_UNAVAILABLE", Message: "Credential value is unavailable or invalid"}
}

// CheckCredential checks local key mechanics only, never remote access. Parsing
// errors can contain key material; always replace them with a sanitized error.
func CheckCredential(kind string, value []byte) error {
	if len(value) == 0 || len(value) > domain.CommandPayloadLimit {
		return credentialUnavailable()
	}
	switch kind {
	case "git-http-token":
		token := strings.TrimSpace(string(value))
		if token == "" || strings.ContainsFunc(token, unicode.IsControl) {
			return credentialUnavailable()
		}
	case "git-ssh-key", "ssh-private-key":
		key, err := ssh.ParsePrivateKey(value)
		if err != nil {
			return credentialUnavailable()
		}
		challenge := []byte("nixmox-credential-check")
		proof, err := key.Sign(rand.Reader, challenge)
		if err != nil || key.PublicKey().Verify(challenge, proof) != nil {
			return credentialUnavailable()
		}
	case "nix-cache-signing-key":
		key, err := signature.LoadSecretKey(strings.TrimSpace(string(value)))
		if err != nil {
			return credentialUnavailable()
		}
		proof, err := key.Sign(rand.Reader, "nixmox-credential-check")
		if err != nil || !key.ToPublicKey().Verify("nixmox-credential-check", proof) {
			return credentialUnavailable()
		}
	case "sops-age-key":
		keys, err := age.ParseIdentities(bytes.NewReader(value))
		if err != nil || len(keys) == 0 {
			return credentialUnavailable()
		}
		for _, key := range keys {
			identity, ok := key.(*age.X25519Identity)
			if !ok || identity.Recipient() == nil {
				return credentialUnavailable()
			}
		}
	default:
		return &domain.Error{Code: "UNSUPPORTED_CAPABILITY", Message: "Unsupported credential kind"}
	}
	return nil
}

// ReadCredential bounds and clears temporary read buffers at the call site.
func ReadCredential(r io.Reader) ([]byte, error) {
	value, err := secretBytes(r)
	if err != nil {
		return nil, credentialUnavailable()
	}
	return value, nil
}
