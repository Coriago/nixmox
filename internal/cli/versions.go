package cli

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"

	"github.com/spf13/cobra"
)

// Version flags and commands share the immutable recipe/configuration URL contract.
func versionFlag(command *cobra.Command) {
	command.Flags().Uint64("version", 0, "Immutable record version")
	_ = command.MarkFlagRequired("version")
}

func versionPath(command *cobra.Command, group, id string) (string, error) {
	version, _ := command.Flags().GetUint64("version")
	if version == 0 {
		return "", errors.New("version must be positive")
	}
	return "/" + group + "s/" + url.PathEscape(id) + "/versions/" + strconv.FormatUint(version, 10), nil
}

func (c console) addVersionedCommands(root *cobra.Command) {
	for _, group := range []string{"recipe", "configuration"} {
		for _, action := range []string{"create", "update"} {
			count, use := 0, action
			if action == "update" {
				count = 1
				use += " ID"
			}
			command := &cobra.Command{
				Use:   use,
				Short: action + " " + group + " version",
				Args:  cobra.ExactArgs(count),
				RunE: func(cmd *cobra.Command, args []string) error {
					data := json.RawMessage(flag(cmd, "data"))
					if !json.Valid(data) {
						return errors.New("--data must be JSON")
					}
					method, path := http.MethodPost, "/"+group+"s"
					if len(args) > 0 {
						method = http.MethodPut
						path += "/" + url.PathEscape(args[0])
					}
					return c.call(cmd, method, path, data)
				},
			}
			required(command, "data", "JSON inputs (updates require expectedVersion)")
			child(root, group).AddCommand(command)
		}
	}
	sync := &cobra.Command{
		Use:   "sync ID",
		Short: "Sync a repository-tracked recipe",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := versionPath(cmd, "recipe", args[0])
			if err != nil {
				return err
			}
			return c.call(cmd, http.MethodPost, path+"/sync", nil)
		},
	}
	versionFlag(sync)
	child(root, "recipe").AddCommand(sync)
	for _, action := range []string{"upgrade", "target"} {
		command := &cobra.Command{
			Use:   action + " ID",
			Short: action + " configuration version",
			Args:  cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				path, err := versionPath(cmd, "configuration", args[0])
				if err != nil {
					return err
				}
				data := json.RawMessage(flag(cmd, "data"))
				if !json.Valid(data) {
					return errors.New("--data must be JSON")
				}
				return c.call(cmd, http.MethodPost, path+"/"+action, data)
			},
		}
		versionFlag(command)
		required(command, "data", "Recipe reference or target repository pin as JSON")
		child(root, "configuration").AddCommand(command)
	}
	for _, action := range []string{"observe", "render", "publish"} {
		command := &cobra.Command{
			Use:   action + " ID",
			Short: action + " configuration version",
			Args:  cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				path, err := versionPath(cmd, "configuration", args[0])
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
			},
		}
		versionFlag(command)
		if action == "observe" {
			command.Flags().String("name", "", "Observation to refresh (all when omitted)")
		}
		if action == "publish" {
			required(command, "mode", "commit or pull-request")
		}
		child(root, "configuration").AddCommand(command)
	}
}
