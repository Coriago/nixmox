// Package addhost renders repository-owned host templates, prepares isolated
// Git/SOPS changes, and performs installer-local checks and installation.
package addhost

import (
	"bytes"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"text/template"
	"unicode/utf8"
)

type Field struct {
	Type     string   `json:"type" yaml:"type"`
	Default  any      `json:"default,omitempty" yaml:"default,omitempty"`
	Values   []string `json:"values,omitempty" yaml:"values,omitempty"`
	Required bool     `json:"required" yaml:"required"`
}

type Config struct {
	SOPS       *SOPSConfig      `json:"sops,omitempty" yaml:"sops,omitempty"`
	AgeKeyFile string           `json:"ageKeyFile,omitempty" yaml:"ageKeyFile,omitempty"`
	Form       map[string]Field `json:"form" yaml:"form"`
	Path       string           `json:"path" yaml:"path"`
	Template   string           `json:"template" yaml:"template"` // Template file path.
}

type SOPSConfig struct {
	RulesFile     string   `json:"rulesFile" yaml:"rulesFile"`
	Files         []string `json:"files" yaml:"files"`
	RulePathRegex string   `json:"rulePathRegex" yaml:"rulePathRegex"`
}

type Disk struct {
	Model     string `json:"model"`
	Serial    string `json:"serial"`
	Size      uint64 `json:"size"`
	Path      string `json:"path"`
	Whole     bool   `json:"whole"`
	Installer bool   `json:"installer"`
}

type Input struct {
	Values         map[string]any `json:"values"`
	Hardware       string         `json:"hardware"`
	ClusterAddress string         `json:"cluster_address"`
	Disks          []Disk         `json:"disks"`
}

type Result struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

var hostname = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?$`)
var username = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)
var fieldName = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*$`)

func (c Config) Validate() error {
	if c.Path == "" || c.Template == "" || len(c.Form) == 0 {
		return fmt.Errorf("add_host requires form, path, and template")
	}
	if c.SOPS != nil {
		if !filepath.IsLocal(c.SOPS.RulesFile) || len(c.SOPS.Files) == 0 || c.SOPS.RulePathRegex == "" {
			return fmt.Errorf("SOPS requires a repository rulesFile, files, and rulePathRegex")
		}
		if _, err := regexp.Compile(c.SOPS.RulePathRegex); err != nil {
			return fmt.Errorf("SOPS rulePathRegex: %w", err)
		}
		for _, file := range c.SOPS.Files {
			if !filepath.IsLocal(file) || strings.HasPrefix(file, "-") {
				return fmt.Errorf("SOPS files must stay inside the repository")
			}
		}
	}
	for name, f := range c.Form {
		if !fieldName.MatchString(name) || name == "Hardware" || name == "ClusterAddress" {
			return fmt.Errorf("invalid or reserved form field %q", name)
		}
		switch f.Type {
		case "hostname", "username", "text", "multiselect", "disk":
		default:
			return fmt.Errorf("field %s: unsupported type %q", name, f.Type)
		}
	}
	return nil
}

// NixString escapes interpolation as well as quoted-string delimiters. Go/JSON
// quoting is unsuitable because Nix does not support their Unicode escapes.
func NixString(s string) (string, error) {
	if !utf8.ValidString(s) {
		return "", fmt.Errorf("invalid UTF-8 string")
	}
	for _, r := range s {
		if r < 32 && r != '\n' && r != '\r' && r != '\t' {
			return "", fmt.Errorf("unsupported control character")
		}
	}
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, "${", `\${`, "\n", `\n`, "\r", `\r`, "\t", `\t`).Replace(s) + `"`, nil
}

func render(source string, values map[string]any) (string, error) {
	t, err := template.New("addHost").Option("missingkey=error").Parse(source)
	if err != nil {
		return "", err
	}
	var b bytes.Buffer
	if err := t.Execute(&b, values); err != nil {
		return "", err
	}
	return b.String(), nil
}

func Generate(c Config, in Input) (Result, error) {
	fail := func(err error) (Result, error) { return Result{}, err }
	if err := c.Validate(); err != nil {
		return fail(err)
	}
	host, port, err := net.SplitHostPort(in.ClusterAddress)
	n, portErr := strconv.Atoi(port)
	if err != nil || host == "" || portErr != nil || n < 1 || n > 65535 {
		return fail(fmt.Errorf("cluster_address must be a host:port"))
	}
	plain, quoted := map[string]any{}, map[string]any{}
	for name := range in.Values {
		if _, ok := c.Form[name]; !ok {
			return fail(fmt.Errorf("unknown field %q", name))
		}
	}
	for name, f := range c.Form {
		value, ok := in.Values[name]
		if !ok {
			value = f.Default
		}
		if f.Type == "multiselect" {
			var choices []string
			switch v := value.(type) {
			case nil:
			case []string:
				choices = v
			case []any:
				for _, item := range v {
					s, ok := item.(string)
					if !ok {
						return fail(fmt.Errorf("%s must contain strings", name))
					}
					choices = append(choices, s)
				}
			default:
				return fail(fmt.Errorf("%s must be a list", name))
			}
			if f.Required && len(choices) == 0 {
				return fail(fmt.Errorf("%s is required", name))
			}
			literals := []string{}
			seen := map[string]bool{}
			for _, s := range choices {
				if !slices.Contains(f.Values, s) || seen[s] {
					return fail(fmt.Errorf("%s: invalid or duplicate choice %q", name, s))
				}
				seen[s] = true
				q, err := NixString(s)
				if err != nil {
					return fail(err)
				}
				literals = append(literals, q)
			}
			plain[name], quoted[name] = choices, literals
			continue
		}
		if value == nil {
			value = ""
		}
		s, ok := value.(string)
		if !ok {
			return fail(fmt.Errorf("%s must be a string", name))
		}
		if f.Required && s == "" {
			return fail(fmt.Errorf("%s is required", name))
		}
		switch f.Type {
		case "hostname":
			if !hostname.MatchString(s) {
				return fail(fmt.Errorf("%s: invalid hostname", name))
			}
		case "username":
			if !username.MatchString(s) {
				return fail(fmt.Errorf("%s: invalid username", name))
			}
		case "disk":
			valid := false
			for _, d := range in.Disks {
				if d.Path == s && d.Whole && !d.Installer && strings.HasPrefix(s, "/dev/") && filepath.Clean(s) == s {
					valid = true
				}
			}
			if !valid {
				return fail(fmt.Errorf("%s: select an available whole disk other than the installer", name))
			}
		}
		q, err := NixString(s)
		if err != nil {
			return fail(err)
		}
		plain[name], quoted[name] = s, q
	}
	destination, err := render(c.Path, plain)
	if err != nil {
		return fail(err)
	}
	if !filepath.IsLocal(destination) || strings.ContainsAny(destination, "\\\x00\n\r") || filepath.Clean(destination) == "." {
		return fail(fmt.Errorf("destination must stay inside the repository"))
	}
	quoted["Hardware"] = in.Hardware
	address, err := NixString(in.ClusterAddress)
	if err != nil {
		return fail(err)
	}
	quoted["ClusterAddress"] = address
	source, err := os.ReadFile(c.Template)
	if err != nil {
		return fail(err)
	}
	// Hardware may be omitted, but referencing it must not silently generate an empty import.
	if in.Hardware == "" {
		delete(quoted, "Hardware")
	}
	content, err := render(string(source), quoted)
	if err != nil {
		return fail(err)
	}
	return Result{Path: filepath.Clean(destination), Content: content}, nil
}

// Write creates exactly one configuration beneath repo, without following links
// outside it or replacing an existing file. os.Root also confines concurrent
// symlink changes; O_EXCL protects both files and dangling destination symlinks.
func Write(repo string, result Result) error {
	if !filepath.IsLocal(result.Path) {
		return fmt.Errorf("destination must stay inside the repository")
	}
	root, err := os.OpenRoot(repo)
	if err != nil {
		return err
	}
	defer root.Close()
	if err := root.MkdirAll(filepath.Dir(result.Path), 0755); err != nil {
		return err
	}
	f, err := root.OpenFile(result.Path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		return err
	}
	_, writeErr := f.WriteString(result.Content)
	closeErr := f.Close()
	if writeErr != nil {
		_ = root.Remove(result.Path)
		return writeErr
	}
	if closeErr != nil {
		_ = root.Remove(result.Path)
		return closeErr
	}
	return nil
}
