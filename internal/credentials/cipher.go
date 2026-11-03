// Package credentials encrypts complete SOPS documents before shared storage.
package credentials

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"filippo.io/age"

	"nixmox/internal/host"
	"nixmox/internal/identity"
)

const MaxSecretSize = 64 << 10

type Recipient struct {
	NodeID       string `json:"nodeId"`
	AgePublicKey string `json:"agePublicKey"`
}

type Recipients struct {
	ClusterID  string      `json:"clusterId"`
	Recipients []Recipient `json:"recipients"`
}

type Record struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Type    string `json:"type"`
	Content string `json:"content"`
}

type Cipher struct{ KeyFile string }

// DeriveIdentity converts the real SSH host key, not the node's TLS key, using
// ssh-to-age. Only the private node directory receives the derived age identity.
func DeriveIdentity(ctx context.Context, sshHostKey, destination string) (string, error) {
	data, err := (host.Process{}).Run(ctx, "", nil, "ssh-to-age", "-private-key", "-i", sshHostKey)
	if err != nil {
		return "", errors.New("could not derive age identity from SSH host key")
	}
	defer clear(data)
	key, err := age.ParseX25519Identity(strings.TrimSpace(string(data)))
	if err != nil {
		return "", errors.New("invalid derived age identity")
	}
	if err = identity.WritePrivate(destination, append([]byte(key.String()), '\n')); err != nil {
		return "", err
	}
	return key.Recipient().String(), nil
}

func recipientKeys(recipients []Recipient) ([]string, error) {
	if len(recipients) == 0 {
		return nil, errors.New("at least one full-member recipient required")
	}
	keys := make([]string, 0, len(recipients))
	nodes := map[string]bool{}
	seen := map[string]bool{}
	for _, recipient := range recipients {
		if recipient.NodeID == "" || nodes[recipient.NodeID] {
			return nil, errors.New("recipient node IDs must be unique and nonempty")
		}
		nodes[recipient.NodeID] = true
		key, err := age.ParseX25519Recipient(recipient.AgePublicKey)
		if err != nil {
			return nil, errors.New("invalid public age key")
		}
		if !seen[key.String()] {
			keys = append(keys, key.String())
			seen[key.String()] = true
		}
	}
	sort.Strings(keys)
	return keys, nil
}

func (c Cipher) run(ctx context.Context, input []byte, args ...string) ([]byte, error) {
	keyPath, err := filepath.Abs(c.KeyFile)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(keyPath)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("private age identity file required (0600)")
	}
	home, err := os.MkdirTemp("", "nixmox-sops-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(home)
	process := host.Process{
		Env: []string{
			"PATH=" + os.Getenv("PATH"),
			"HOME=" + home,
			"XDG_CONFIG_HOME=" + home,
			"SOPS_AGE_KEY_FILE=" + keyPath,
		},
	}
	output, err := process.Run(ctx, home, bytes.NewReader(input), "sops", args...)
	if err != nil {
		clear(output)
		return nil, errors.New("SOPS operation failed")
	}
	return output, nil
}

func (c Cipher) Encrypt(ctx context.Context, secret []byte, recipients []Recipient) (string, error) {
	if len(secret) > MaxSecretSize {
		return "", errors.New("credential exceeds 64 KiB")
	}
	keys, err := recipientKeys(recipients)
	if err != nil {
		return "", err
	}
	// []byte is represented as base64, preserving arbitrary secret bytes.
	data, err := json.Marshal(struct {
		Value []byte `json:"value"`
	}{secret})
	if err != nil {
		return "", err
	}
	defer clear(data)
	encrypted, err := c.run(ctx, data, "encrypt", "--filename-override", "credential.json", "--input-type", "json", "--output-type", "json", "--age", strings.Join(keys, ","))
	if err != nil {
		return "", err
	}
	return string(encrypted), nil
}

func (c Cipher) Decrypt(ctx context.Context, document string) ([]byte, error) {
	if len(document) > 4*MaxSecretSize {
		return nil, errors.New("credential document too large")
	}
	decrypted, err := c.run(ctx, []byte(document), "decrypt", "--filename-override", "credential.json", "--input-type", "json", "--output-type", "json")
	if err != nil {
		return nil, err
	}
	defer clear(decrypted)
	var payload struct {
		Value []byte `json:"value"`
	}
	if err = json.Unmarshal(decrypted, &payload); err != nil {
		return nil, errors.New("invalid credential document")
	}
	if len(payload.Value) > MaxSecretSize {
		clear(payload.Value)
		return nil, errors.New("decrypted credential too large")
	}
	return payload.Value, nil
}

// Rekey rotates the data key as well as recipients, retaining a complete SOPS
// document. The coordinator must rewrite every record before committing the new
// global recipients; publishing only a recipient list never revokes access.
func (c Cipher) Rekey(ctx context.Context, document string, recipients []Recipient) (string, error) {
	secret, err := c.Decrypt(ctx, document)
	if err != nil {
		return "", err
	}
	defer clear(secret)
	return c.Encrypt(ctx, secret, recipients)
}
