package addhost

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// Repository prepares changes in a private clone. Neither the running GitOps
// checkout nor remote history is rewritten. A rejected push stops onboarding.
type Repository struct {
	URL, Branch, Directory, SSHKey, Netrc, AgeKey string
}

func ValidateRepositoryURL(value string) error {
	u, err := url.Parse(value)
	if err == nil && (u.Scheme == "https" || u.Scheme == "http") && (u.User != nil || u.RawQuery != "") {
		return fmt.Errorf("use a Git credential file instead of embedding credentials in the repository URL")
	}
	return nil
}

func (r Repository) command(ctx context.Context, program string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, program, args...)
	cmd.Dir = r.Directory
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'" }
	if r.SSHKey != "" {
		cmd.Env = append(cmd.Env, "GIT_SSH_COMMAND=ssh -o BatchMode=yes -i "+quote(r.SSHKey))
	}
	if r.Netrc != "" {
		cmd.Env = append(cmd.Env, "GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=credential.helper", "GIT_CONFIG_VALUE_0=netrc -f "+quote(r.Netrc))
	}
	if r.AgeKey != "" {
		cmd.Env = append(cmd.Env, "SOPS_AGE_KEY_FILE="+r.AgeKey)
	}
	// Error output may contain credential-bearing URLs or decrypted information.
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("%s failed: %w", program, err)
	}
	return out, nil
}

func (r Repository) Publish(ctx context.Context, c Config, result Result, recipient string, out io.Writer) (string, error) {
	if err := ValidateRepositoryURL(r.URL); err != nil {
		return "", err
	}
	if r.URL == "" || r.Branch == "" || strings.HasPrefix(r.Branch, "-") {
		return "", fmt.Errorf("Git repository and branch are required")
	}
	parent := filepath.Dir(r.Directory)
	if err := os.MkdirAll(parent, 0700); err != nil {
		return "", err
	}
	clone := r
	clone.Directory = parent
	fmt.Fprintln(out, "Cloning repository into an isolated checkout")
	if _, err := clone.command(ctx, "git", "clone", "--single-branch", "--branch", r.Branch, "--", r.URL, r.Directory); err != nil {
		return "", fmt.Errorf("clone failed; check repository read credentials and branch: %w", err)
	}
	if err := Write(r.Directory, result); err != nil {
		return "", err
	}
	files := []string{result.Path}
	if c.SOPS != nil {
		if c.SOPS.RulesFile == result.Path {
			return "", fmt.Errorf("host destination overlaps SOPS rules")
		}
		for _, file := range c.SOPS.Files {
			if file == result.Path {
				return "", fmt.Errorf("host destination overlaps encrypted file")
			}
		}
		fmt.Fprintln(out, "Adding installer public recipient and updating encrypted file keys")
		if !regexp.MustCompile(`^age1[0-9a-z]+$`).MatchString(recipient) {
			return "", fmt.Errorf("installer age recipient is invalid")
		}
		if err := UpdateRules(r.Directory, *c.SOPS, recipient); err != nil {
			return "", err
		}
		for _, file := range c.SOPS.Files {
			if err := regularRepositoryFile(r.Directory, file); err != nil {
				return "", err
			}
			if _, err := r.command(ctx, "sops", "--config", c.SOPS.RulesFile, "updatekeys", "--yes", file); err != nil {
				return "", fmt.Errorf("update SOPS recipients for %s failed; check age identity authorization: %w", file, err)
			}
		}
		files = append(files, c.SOPS.RulesFile)
		files = append(files, c.SOPS.Files...)
	}
	if _, err := r.command(ctx, "git", append([]string{"add", "--"}, files...)...); err != nil {
		return "", err
	}
	if _, err := r.command(ctx, "git", "-c", "user.name=nixmox", "-c", "user.email=nixmox@localhost", "commit", "-m", "Add host configuration "+result.Path); err != nil {
		return "", err
	}
	sha, err := r.command(ctx, "git", "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	fmt.Fprintln(out, "Pushing host configuration and encrypted recipient updates together")
	if _, err := r.command(ctx, "git", "push", "origin", "HEAD:refs/heads/"+r.Branch); err != nil {
		return "", fmt.Errorf("push failed; check write permission, branch protection, or concurrent updates: %w", err)
	}
	return strings.TrimSpace(string(sha)), nil
}

func regularRepositoryFile(repo, path string) error {
	if !filepath.IsLocal(path) || strings.HasPrefix(path, "-") {
		return fmt.Errorf("invalid repository file path")
	}
	// External tools do not use os.Root, so reject symlinks in every component.
	current := repo
	for _, part := range strings.Split(filepath.Clean(path), string(filepath.Separator)) {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlink in repository path %s", path)
		}
	}
	info, err := os.Stat(current)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("not a regular file: %s", path)
	}
	return nil
}

// Edit the YAML node tree so unrelated rules, comments, and anchored key lists
// survive. An alias is materialized only for the selected rule's age value.
func UpdateRules(repo string, c SOPSConfig, recipient string) error {
	if len(c.Files) == 0 || c.RulePathRegex == "" {
		return fmt.Errorf("SOPS files and rulePathRegex are required")
	}
	if err := regularRepositoryFile(repo, c.RulesFile); err != nil {
		return err
	}
	path := filepath.Join(repo, c.RulesFile)
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return err
	}
	// Materialize aliases before editing so an anchored recipient list shared
	// by other rules cannot accidentally grant this host access to those rules.
	var materialize func(*yaml.Node, int) (*yaml.Node, error)
	materialize = func(n *yaml.Node, depth int) (*yaml.Node, error) {
		if depth > 64 {
			return nil, fmt.Errorf("SOPS YAML alias nesting is too deep")
		}
		if n.Kind == yaml.AliasNode {
			return materialize(n.Alias, depth+1)
		}
		copy := *n
		copy.Anchor = ""
		copy.Content = nil
		for _, child := range n.Content {
			v, err := materialize(child, depth+1)
			if err != nil {
				return nil, err
			}
			copy.Content = append(copy.Content, v)
		}
		return &copy, nil
	}
	resolved, err := materialize(&doc, 0)
	if err != nil {
		return err
	}
	doc = *resolved
	if len(doc.Content) != 1 {
		return fmt.Errorf("invalid SOPS rules file")
	}
	mapping := func(node *yaml.Node, key string) *yaml.Node {
		for i := 0; i+1 < len(node.Content); i += 2 {
			if node.Content[i].Value == key {
				return node.Content[i+1]
			}
		}
		return nil
	}
	rules := mapping(doc.Content[0], "creation_rules")
	if rules == nil || rules.Kind != yaml.SequenceNode {
		return fmt.Errorf("missing SOPS creation_rules")
	}
	var selected *yaml.Node
	for _, rule := range rules.Content {
		pattern := mapping(rule, "path_regex")
		if pattern != nil && pattern.Value == c.RulePathRegex {
			if selected != nil {
				return fmt.Errorf("ambiguous SOPS rulePathRegex")
			}
			selected = rule
		}
	}
	if selected == nil {
		return fmt.Errorf("selected SOPS rule not found")
	}
	for _, file := range c.Files {
		rulePath, err := filepath.Rel(filepath.Dir(c.RulesFile), file)
		if err != nil {
			return err
		}
		matched := false
		for _, rule := range rules.Content {
			pattern := mapping(rule, "path_regex")
			if pattern == nil {
				if rule != selected {
					return fmt.Errorf("an earlier SOPS catch-all rule selects %s", file)
				}
				matched = true
				break
			}
			re, err := regexp.Compile(pattern.Value)
			if err != nil {
				return err
			}
			if re.MatchString(filepath.ToSlash(rulePath)) {
				if rule != selected {
					return fmt.Errorf("a different SOPS rule selects %s", file)
				}
				matched = true
				break
			}
		}
		if !matched {
			return fmt.Errorf("selected SOPS rule does not match %s", file)
		}
	}
	if mapping(selected, "key_groups") != nil {
		return fmt.Errorf("SOPS key_groups is unsupported; select a rule with an age recipient list")
	}
	age := mapping(selected, "age")
	if age == nil {
		age = &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str"}
		selected.Content = append(selected.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "age"}, age)
	}
	if age.Kind == yaml.AliasNode {
		copy := *age.Alias
		copy.Anchor = ""
		*age = copy
	}
	if age.Kind != yaml.ScalarNode {
		return fmt.Errorf("SOPS age must be a comma-separated string")
	}
	found := false
	for _, key := range strings.Split(age.Value, ",") {
		if strings.TrimSpace(key) == recipient {
			found = true
		}
	}
	if !found {
		if strings.TrimSpace(age.Value) != "" {
			age.Value = strings.TrimSpace(age.Value) + ",\n"
		}
		age.Value += recipient
	}
	encoded, err := yaml.Marshal(&doc)
	if err != nil {
		return err
	}
	return os.WriteFile(path, encoded, 0600)
}
