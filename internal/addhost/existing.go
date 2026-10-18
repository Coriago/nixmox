package addhost

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// CheckoutExisting never commits or pushes. A selection is restricted to a
// commit reachable from the configured branch, then checked out detached.
func (r Repository) CheckoutExisting(ctx context.Context, revision string) (string, error) {
	if err := ValidateRepositoryURL(r.URL); err != nil {
		return "", err
	}
	if r.URL == "" || r.Branch == "" || strings.HasPrefix(r.Branch, "-") {
		return "", fmt.Errorf("repository and branch are required")
	}
	if revision != "" && !regexp.MustCompile(`^[0-9a-f]{40,64}$`).MatchString(revision) {
		return "", fmt.Errorf("select a full Git commit SHA")
	}
	clone := r
	clone.Directory = filepath.Dir(r.Directory)
	if _, err := clone.command(ctx, "git", "clone", "--single-branch", "--branch", r.Branch, "--", r.URL, r.Directory); err != nil {
		return "", fmt.Errorf("read repository: %w", err)
	}
	if revision != "" {
		if _, err := r.command(ctx, "git", "merge-base", "--is-ancestor", revision, "HEAD"); err != nil {
			return "", fmt.Errorf("revision is not available on the configured branch")
		}
		if _, err := r.command(ctx, "git", "checkout", "--detach", revision); err != nil {
			return "", err
		}
	}
	sha, err := r.command(ctx, "git", "rev-parse", "HEAD")
	return strings.TrimSpace(string(sha)), err
}

func ExistingConfigurations(ctx context.Context, checkout, directory string) ([]string, error) {
	if directory != "" && !filepath.IsLocal(directory) {
		return nil, fmt.Errorf("invalid flake directory")
	}
	cmd := exec.CommandContext(ctx, "nix", "eval", "--no-write-lock-file", "--json", "--apply", "builtins.attrNames", "path:"+filepath.Join(checkout, directory)+"#nixosConfigurations")
	data, err := cmd.Output()
	if err != nil {
		return nil, evaluationError("list existing configurations", err)
	}
	var names []string
	if err := json.Unmarshal(data, &names); err != nil {
		return nil, err
	}
	return names, nil
}

func evaluationError(action string, err error) error {
	if e, ok := err.(*exec.ExitError); ok {
		message := strings.TrimSpace(string(e.Stderr))
		if len(message) > 8192 {
			message = message[len(message)-8192:]
		}
		if message != "" {
			return fmt.Errorf("%s: %s", action, message)
		}
	}
	return fmt.Errorf("%s: %w", action, err)
}

// Existing selections do not rewrite secrets; the installer must already be
// an authorized recipient in every configured encrypted file.
func CheckExistingRecipients(checkout string, c *SOPSConfig, recipient string) error {
	if c == nil {
		return nil
	}
	type ageKey struct {
		Recipient string `yaml:"recipient"`
	}
	for _, file := range c.Files {
		if err := regularRepositoryFile(checkout, file); err != nil {
			return err
		}
		data, err := os.ReadFile(filepath.Join(checkout, file))
		if err != nil {
			return err
		}
		var doc struct {
			SOPS struct {
				Age    []ageKey `yaml:"age"`
				Groups []struct {
					Age []ageKey `yaml:"age"`
				} `yaml:"key_groups"`
			} `yaml:"sops"`
		}
		if err := yaml.Unmarshal(data, &doc); err != nil {
			return err
		}
		keys := doc.SOPS.Age
		if len(doc.SOPS.Groups) > 1 {
			return fmt.Errorf("%s uses multiple SOPS key groups; cannot verify installer access", file)
		}
		if len(doc.SOPS.Groups) == 1 {
			keys = append(keys, doc.SOPS.Groups[0].Age...)
		}
		found := false
		for _, key := range keys {
			found = found || key.Recipient == recipient
		}
		if !found {
			return fmt.Errorf("%s does not authorize installer age recipient %s; enable “Authorize this installer’s new key in SOPS” to update recipients from the UI", file, recipient)
		}
	}
	return nil
}
