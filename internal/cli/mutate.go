package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"

	"github.com/charmbracelet/x/term"
	"github.com/spf13/cobra"
	"nixmox/internal/credentials"
)

func (c console) addMutations(root *cobra.Command) {
	create := &cobra.Command{Use: "create", Short: "Create a cluster on an unjoined host", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		return c.call(cmd, http.MethodPost, "/cluster", map[string]any{"name": flag(cmd, "name")})
	}}
	required(create, "name", "Cluster name")
	child(root, "cluster").AddCommand(create)
	for _, action := range []string{"destroy", "recover"} {
		command := &cobra.Command{Use: action, Short: action + " cluster (explicit confirmation required)", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
			return c.call(cmd, http.MethodPost, "/cluster/"+action, map[string]any{"confirm": flag(cmd, "confirm")})
		}}
		required(command, "confirm", "Exact cluster ID")
		child(root, "cluster").AddCommand(command)
	}
	for _, action := range []string{"promote", "demote", "remove", "reboot", "shutdown", "restart-daemon"} {
		command := &cobra.Command{Use: action + " ID", Short: action + " host", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
			payload := map[string]any{"force": boolean(cmd, "force")}
			if cmd.Flags().Lookup("to") != nil {
				target := flag(cmd, "to")
				if (action == "promote" && target != "full" && target != "full-stable") || (action == "demote" && target != "full" && target != "pending") {
					return errors.New("invalid membership target")
				}
				payload["toStatus"] = target
			}
			if cmd.Flags().Lookup("evacuate") != nil {
				payload["evacuate"] = boolean(cmd, "evacuate")
			}
			return c.call(cmd, http.MethodPost, "/hosts/"+url.PathEscape(args[0])+"/"+action, payload)
		}}
		command.Flags().Bool("force", false, "Force the operation (server quorum invariants still apply)")
		if action == "promote" || action == "demote" {
			required(command, "to", "Target membership status")
		}
		if action == "reboot" || action == "shutdown" || action == "restart-daemon" {
			command.Flags().Bool("evacuate", false, "Move replicas before stopping the host")
		}
		child(root, "host").AddCommand(command)
	}
	assignment := &cobra.Command{Use: "configuration ID", Short: "Assign a host configuration", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		return c.call(cmd, http.MethodPut, "/hosts/"+url.PathEscape(args[0])+"/configuration", map[string]any{"configurationId": flag(cmd, "configuration")})
	}}
	required(assignment, "configuration", "Configuration ID")
	child(root, "host").AddCommand(assignment)
	for _, action := range []struct{ group, name, method, path, suffix string }{
		{"removal", "clear", http.MethodDelete, "/removals/", ""},
		{"repo", "remove", http.MethodDelete, "/repos/", ""}, {"repo", "refresh", http.MethodPost, "/repos/", "/refresh"},
		{"credential", "remove", http.MethodDelete, "/credentials/", ""},
		{"configuration", "refresh", http.MethodPost, "/configurations/", "/refresh"},
		{"task", "cancel", http.MethodPost, "/tasks/", "/cancel"}, {"task", "retry", http.MethodPost, "/tasks/", "/retry"},
		{"enrollment", "approve", http.MethodPost, "/enrollment/", "/approve"}, {"enrollment", "reject", http.MethodPost, "/enrollment/", "/reject"},
	} {
		child(root, action.group).AddCommand(&cobra.Command{Use: action.name + " ID", Short: action.name + " record", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
			return c.call(cmd, action.method, action.path+url.PathEscape(args[0])+action.suffix, nil)
		}})
	}
	child(child(root, "enrollment"), "rejection").AddCommand(&cobra.Command{Use: "clear ID", Short: "Clear an enrollment rejection", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		return c.call(cmd, http.MethodDelete, "/enrollment/rejections/"+url.PathEscape(args[0]), nil)
	}})
	for _, action := range []string{"request", "add"} {
		command := &cobra.Command{Use: action, Short: "Request enrollment", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
			payload := map[string]any{}
			for flagName, jsonName := range map[string]string{"discovery": "discoveryId", "address": "address", "contact": "contactHostId"} {
				if cmd.Flags().Lookup(flagName) != nil && flag(cmd, flagName) != "" {
					payload[jsonName] = flag(cmd, flagName)
				}
			}
			return c.call(cmd, http.MethodPost, "/enrollment/request", payload)
		}}
		command.Flags().String("discovery", "", "Discovery record ID")
		command.Flags().String("address", "", "Explicit peer HTTP endpoint")
		command.MarkFlagsOneRequired("discovery", "address")
		command.MarkFlagsMutuallyExclusive("discovery", "address")
		if action == "add" {
			required(command, "contact", "Full-member contact host ID")
		}
		child(root, "enrollment").AddCommand(command)
	}
	for _, action := range []string{"add", "update"} {
		count := 0
		use := action
		if action == "update" {
			count = 1
			use += " ID"
		}
		command := &cobra.Command{Use: use, Short: action + " tracked repository", Args: cobra.ExactArgs(count), RunE: func(cmd *cobra.Command, args []string) error {
			input, err := definition(cmd)
			if err != nil {
				return err
			}
			method, path := http.MethodPost, "/repos"
			if len(args) > 0 {
				method = http.MethodPatch
				path += "/" + url.PathEscape(args[0])
			}
			return c.call(cmd, method, path, input)
		}}
		required(command, "file", "JSON or YAML repository definition")
		child(root, "repo").AddCommand(command)
	}
	approve := &cobra.Command{Use: "approve ID", Short: "Approve the current task review", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		return c.call(cmd, http.MethodPost, "/tasks/"+url.PathEscape(args[0])+"/approve", map[string]any{"outputId": flag(cmd, "output")})
	}}
	required(approve, "output", "Current review output ID")
	child(root, "task").AddCommand(approve)
	c.addRecipes(root)
	c.addHostJobs(root)
	c.addSecrets(root)
}

func versionFlag(command *cobra.Command) {
	command.Flags().Uint64("version", 0, "Immutable recipe version")
	_ = command.MarkFlagRequired("version")
}
func recipePath(command *cobra.Command, id string) (string, error) {
	version, _ := command.Flags().GetUint64("version")
	if version == 0 {
		return "", errors.New("recipe version must be positive")
	}
	return "/recipes/" + url.PathEscape(id) + "/versions/" + strconv.FormatUint(version, 10), nil
}
func (c console) addRecipes(root *cobra.Command) {
	for _, action := range []string{"create", "update"} {
		count := 0
		use := action
		if action == "update" {
			count = 1
			use += " ID"
		}
		command := &cobra.Command{Use: use, Short: action + " recipe version", Args: cobra.ExactArgs(count), RunE: func(cmd *cobra.Command, args []string) error {
			data := json.RawMessage(flag(cmd, "data"))
			if !json.Valid(data) {
				return errors.New("--data must be JSON")
			}
			method, path := http.MethodPost, "/recipes"
			if len(args) > 0 {
				method = http.MethodPut
				path += "/" + url.PathEscape(args[0])
			}
			return c.call(cmd, method, path, data)
		}}
		required(command, "data", "Recipe fields as JSON")
		child(root, "recipe").AddCommand(command)
	}
	for _, action := range []string{"observe", "render", "publish"} {
		command := &cobra.Command{Use: action + " ID", Short: action + " recipe version", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
			path, err := recipePath(cmd, args[0])
			if err != nil {
				return err
			}
			payload := map[string]any{}
			if action == "observe" && flag(cmd, "name") != "" {
				payload["name"] = flag(cmd, "name")
			}
			if action == "publish" {
				mode := flag(cmd, "mode")
				if mode != "commit" && mode != "pull-request" {
					return errors.New("publication mode must be commit or pull-request")
				}
				payload["mode"] = mode
			}
			return c.call(cmd, http.MethodPost, path+"/"+action, payload)
		}}
		versionFlag(command)
		if action == "observe" {
			command.Flags().String("name", "", "Observation to refresh (all when omitted)")
		}
		if action == "publish" {
			required(command, "mode", "commit or pull-request")
		}
		child(root, "recipe").AddCommand(command)
	}
}

func (c console) addHostJobs(root *cobra.Command) {
	build := &cobra.Command{Use: "build", Short: "Build a pinned Nix installable", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		input, err := definition(cmd)
		if err != nil {
			return err
		}
		return c.call(cmd, http.MethodPost, "/hosts/"+url.PathEscape(flag(cmd, "host"))+"/builds", input)
	}}
	required(build, "host", "Executing host ID")
	required(build, "file", "JSON or YAML build inputs")
	root.AddCommand(build)
	for _, action := range []string{"deploy", "install"} {
		command := &cobra.Command{Use: action, Short: action + " a pinned configuration or recipe", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
			payload := map[string]any{}
			if configuration := flag(cmd, "configuration"); configuration != "" {
				payload["configurationId"] = configuration
			} else {
				version, _ := cmd.Flags().GetUint64("version")
				if version == 0 {
					return errors.New("--recipe requires a positive --version")
				}
				payload["recipeId"] = flag(cmd, "recipe")
				payload["recipeVersion"] = version
			}
			if buildHost := flag(cmd, "build-host"); buildHost != "" {
				payload["buildHostId"] = buildHost
			}
			route := "installations"
			if action == "deploy" {
				route = "deployments"
				payload["autoApprove"] = boolean(cmd, "approve")
			}
			return c.call(cmd, http.MethodPost, "/hosts/"+url.PathEscape(flag(cmd, "host"))+"/"+route, payload)
		}}
		required(command, "host", "Target host ID")
		command.Flags().String("configuration", "", "Configuration ID")
		command.Flags().String("recipe", "", "Recipe ID")
		command.Flags().Uint64("version", 0, "Recipe version")
		command.Flags().String("build-host", "", "Optional build host ID")
		command.MarkFlagsOneRequired("configuration", "recipe")
		command.MarkFlagsMutuallyExclusive("configuration", "recipe")
		command.MarkFlagsMutuallyExclusive("configuration", "version")
		command.MarkFlagsRequiredTogether("recipe", "version")
		if action == "deploy" {
			command.Flags().Bool("approve", false, "Automatically approve deployment diff")
		}
		root.AddCommand(command)
	}
	rollback := &cobra.Command{Use: "rollback", Short: "Activate the previous retained generation", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		return c.call(cmd, http.MethodPost, "/hosts/"+url.PathEscape(flag(cmd, "host"))+"/rollbacks", nil)
	}}
	required(rollback, "host", "Target host ID")
	root.AddCommand(rollback)
	media := &cobra.Command{Use: "media", Short: "Create installer media"}
	root.AddCommand(media)
	for _, action := range []string{"launch", "create"} {
		command := &cobra.Command{Use: action, Short: action + " installer", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
			payload := map[string]any{"installerId": flag(cmd, "installer")}
			if buildHost := flag(cmd, "build-host"); buildHost != "" {
				payload["buildHostId"] = buildHost
			}
			route := "launch-installer"
			if action == "create" {
				route = "boot-media"
				payload["device"] = flag(cmd, "device")
			}
			return c.call(cmd, http.MethodPost, "/hosts/"+url.PathEscape(flag(cmd, "host"))+"/"+route, payload)
		}}
		required(command, "host", "Target host ID")
		required(command, "installer", "Installer definition ID")
		command.Flags().String("build-host", "", "Build host ID")
		if action == "create" {
			required(command, "device", "Selected drive path (will be overwritten)")
			media.AddCommand(command)
		} else {
			child(root, "installer").AddCommand(command)
		}
	}
}

func secretInput(cmd *cobra.Command, stdin bool) ([]byte, error) {
	if stdin {
		value, err := io.ReadAll(io.LimitReader(cmd.InOrStdin(), credentials.MaxSecretSize+1))
		if err != nil {
			clear(value)
			return nil, err
		}
		if len(value) > credentials.MaxSecretSize {
			clear(value)
			return nil, errors.New("secret exceeds 64 KiB")
		}
		return value, nil
	}
	file, ok := cmd.InOrStdin().(interface{ Fd() uintptr })
	if !ok || !term.IsTerminal(file.Fd()) {
		return nil, errors.New("hidden prompt requires a terminal; use --stdin for credentials")
	}
	fmt.Fprint(cmd.ErrOrStderr(), "Secret: ")
	value, err := term.ReadPassword(file.Fd())
	fmt.Fprintln(cmd.ErrOrStderr())
	if err != nil {
		clear(value)
		return nil, err
	}
	if len(value) > credentials.MaxSecretSize {
		clear(value)
		return nil, errors.New("secret exceeds 64 KiB")
	}
	return value, err
}
func (c console) addSecrets(root *cobra.Command) {
	put := &cobra.Command{Use: "put", Short: "Encrypt and store a credential", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		value, err := secretInput(cmd, boolean(cmd, "stdin"))
		if err != nil {
			return err
		}
		defer clear(value)
		return c.call(cmd, http.MethodPost, "/credentials", map[string]any{"name": flag(cmd, "name"), "type": flag(cmd, "type"), "value": value})
	}}
	required(put, "name", "Credential name")
	required(put, "type", "Credential type")
	put.Flags().Bool("stdin", false, "Read secret bytes from stdin instead of a hidden prompt")
	child(root, "credential").AddCommand(put)
	connect := &cobra.Command{Use: "connect", Short: "Connect local Wi-Fi (password is not a command argument)", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		value, err := secretInput(cmd, false)
		if err != nil {
			return err
		}
		defer clear(value)
		return c.call(cmd, http.MethodPost, "/network/wifi/connect", map[string]any{"ssid": flag(cmd, "ssid"), "interface": flag(cmd, "interface"), "password": string(value)})
	}}
	required(connect, "ssid", "Network SSID")
	connect.Flags().String("interface", "", "Network interface")
	child(child(root, "network"), "wifi").AddCommand(connect)
}
