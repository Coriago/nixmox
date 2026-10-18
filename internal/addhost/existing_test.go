package addhost

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExistingRecipientsRequireAlreadyEncryptedAccess(t *testing.T) {
	dir := t.TempDir()
	c := &SOPSConfig{Files: []string{"secrets.yaml"}}
	for name, data := range map[string]string{
		"flat":            "sops:\n  age:\n    - recipient: age1installer\n",
		"group":           "sops:\n  key_groups:\n    - age:\n        - recipient: age1installer\n",
		"missing":         "sops:\n  age:\n    - recipient: age1other\n",
		"no-metadata":     "password: ENC[example]\n",
		"multiple-groups": "sops:\n  key_groups:\n    - age:\n        - recipient: age1installer\n    - age:\n        - recipient: age1other\n",
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(dir, "secrets.yaml")
			if err := os.WriteFile(path, []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
			err := CheckExistingRecipients(dir, c, "age1installer")
			valid := name == "flat" || name == "group"
			if (err == nil) != valid {
				t.Fatal(name, err)
			}
			after, _ := os.ReadFile(path)
			if string(after) != data {
				t.Fatal("modified encrypted file")
			}
		})
	}
}
