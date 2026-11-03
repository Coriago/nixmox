// Package prepare owns checks and execution for entering an installer.
package prepare

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/nix-community/go-nix/pkg/storepath"

	"nixmox/internal/installer/installers"
)

type Definition struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Method string `json:"method"`
}

// Boot is a private, already staged kernel/initrd with the preservation payload.
// The launch coordinator must persist recovery state before Execute is called.
type Boot struct {
	Kernel      string
	Initrd      string
	CommandLine string
	SystemPath  string
}

func (b Boot) Validate() error {
	if !storepath.PathRe.MatchString(b.SystemPath) {
		return errors.New("expected installer system store path required")
	}
	if b.CommandLine == "" || len(b.CommandLine) > 4096 || strings.ContainsAny(b.CommandLine, "\x00\r\n") {
		return errors.New("valid installer kernel command line required")
	}
	for _, path := range []string{b.Kernel, b.Initrd} {
		if !filepath.IsAbs(path) {
			return errors.New("absolute staged boot paths required")
		}
		info, err := os.Stat(path)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Size() == 0 {
			return errors.New("nonempty kernel and initrd files required")
		}
	}
	return nil
}

type Method interface {
	Definition() Definition
	Check(installers.Target, installers.Definition) installers.Check
	Execute(context.Context, Boot) error
}
