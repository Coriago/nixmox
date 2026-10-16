package media

import (
	"bytes"
	"encoding/json"
	"fmt"
	"golang.org/x/crypto/ssh"
	"strings"
)

// BuildArgs never invokes a shell. JSON string literals are also valid Nix strings
// after escaping Nix's interpolation marker.
func nixString(s string) string {
	var b bytes.Buffer
	encoder := json.NewEncoder(&b)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(s)
	return strings.ReplaceAll(strings.TrimSpace(b.String()), "${", `\${`)
}
func NormalizeKey(key string) (string, error) {
	if strings.TrimSpace(key) == "" {
		return "", nil
	}
	if len(key) > 8192 {
		return "", fmt.Errorf("SSH public key is too long")
	}
	parsed, _, options, rest, err := ssh.ParseAuthorizedKey([]byte(key))
	if err != nil || len(options) > 0 || strings.TrimSpace(string(rest)) != "" {
		return "", fmt.Errorf("enter one SSH public key without authorized_keys options")
	}
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(parsed))), nil
}
func BuildArgs(flake, key, link string) []string {
	args := []string{"build", "--no-update-lock-file", "--out-link", link}
	if key == "" {
		return append(args, flake)
	}
	source, _, _ := strings.Cut(flake, "#")
	expr := fmt.Sprintf(`let source = builtins.getFlake %s; in (source.lib.mkInstaller { system = "x86_64-linux"; format = "iso"; sshKeys = [ %s ]; }).config.system.build.isoImage`, nixString(source), nixString(key))
	return append(args, "--impure", "--expr", expr)
}
