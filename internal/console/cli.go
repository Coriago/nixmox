package console

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"nixmox/internal/runtime"
	"os"
)

const Version = "0.1.0-dev"

func Run(ctx context.Context, args []string, stdout, stderr io.Writer, serve func(context.Context, runtime.Config) error) int {
	flags := flag.NewFlagSet("nixmox", flag.ContinueOnError)
	flags.SetOutput(stderr)
	endpoint := flags.String("endpoint", "", "console HTTP endpoint")
	version := flags.Bool("version", false, "print version")
	flags.Usage = func() {
		fmt.Fprintln(stderr, "Usage: nixmox [--endpoint URL] <command>")
		fmt.Fprintln(stderr, "Scaffold: engines and feature commands are not yet available.")
		fmt.Fprintln(stderr, "Entry points: serve --config FILE | tui [--installer]")
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if *version {
		fmt.Fprintln(stdout, Version)
		return 0
	}
	rest := flags.Args()
	if len(rest) == 0 {
		flags.Usage()
		return 2
	}
	// This resolves only the console connection, never a remote default host.
	_ = runtime.Endpoint(*endpoint, os.Getenv("NIXMOX_ENDPOINT"), nil)
	var err error
	switch rest[0] {
	case "serve":
		local := flag.NewFlagSet("serve", flag.ContinueOnError)
		local.SetOutput(stderr)
		path := local.String("config", "", "runtime YAML configuration")
		if e := local.Parse(rest[1:]); e != nil {
			if errors.Is(e, flag.ErrHelp) {
				return 0
			}
			return 2
		}
		if local.NArg() != 0 {
			fmt.Fprintln(stderr, "serve takes no positional arguments")
			return 2
		}
		var input io.Reader
		if *path != "" {
			file, e := os.Open(*path)
			if e != nil {
				fmt.Fprintln(stderr, e)
				return 2
			}
			defer file.Close()
			input = file
		}
		config, e := runtime.Load(input, nil)
		if e != nil {
			fmt.Fprintln(stderr, e)
			return 2
		}
		err = serve(ctx, config)
	case "tui":
		local := flag.NewFlagSet("tui", flag.ContinueOnError)
		local.SetOutput(stderr)
		installer := local.Bool("installer", false, "start at network/onboarding")
		if e := local.Parse(rest[1:]); e != nil {
			if errors.Is(e, flag.ErrHelp) {
				return 0
			}
			return 2
		}
		if local.NArg() != 0 {
			fmt.Fprintln(stderr, "tui takes no positional arguments")
			return 2
		}
		err = (Terminal{}).Run(ctx, *installer)
	default:
		fmt.Fprintf(stderr, "Unknown or unimplemented command: %s\n", rest[0])
		return 2
	}
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 3
	}
	return 0
}
