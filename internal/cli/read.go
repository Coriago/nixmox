package cli

import (
	"net/http"
	"net/url"
	"strconv"

	"github.com/spf13/cobra"
)

func (c console) get(use, description, path string, args int) *cobra.Command {
	return &cobra.Command{
		Use:   use,
		Short: description,
		Args:  cobra.ExactArgs(args),
		RunE: func(cmd *cobra.Command, values []string) error {
			target := path
			for _, value := range values {
				target += "/" + url.PathEscape(value)
			}
			query := url.Values{}
			for _, name := range []string{"host", "status", "type", "interface", "cursor"} {
				if cmd.Flags().Lookup(name) != nil && flag(cmd, name) != "" {
					query.Set(name, flag(cmd, name))
				}
			}
			if cmd.Flags().Lookup("limit") != nil {
				limit, _ := cmd.Flags().GetInt("limit")
				query.Set("limit", strconv.Itoa(limit))
			}
			if len(query) > 0 {
				target += "?" + query.Encode()
			}
			return c.call(cmd, http.MethodGet, target, nil)
		},
	}
}

func (c console) addReads(root *cobra.Command) {
	for _, resource := range []struct {
		name, path string
		show       bool
	}{
		{"cluster", "/cluster", false},
		{"host", "/hosts", true},
		{"repo", "/repos", true},
		{"credential", "/credentials", true},
		{"configuration", "/configurations", false},
		{"recipe", "/recipes", false},
		{"task", "/tasks", true},
		{"discovery", "/discovery", false},
		{"enrollment", "/enrollment", true},
		{"removal", "/removals", false},
		{"installer", "/installers", true},
		{"install-prepare", "/install-prepare", false},
		{"install-finalize", "/install-finalize", false},
		{"cache", "/cache-info", false},
	} {
		group := &cobra.Command{Use: resource.name, Short: "Manage " + resource.name}
		if resource.name == "cluster" || resource.name == "cache" {
			group.AddCommand(c.get("show", "Show "+resource.name, resource.path, 0))
		} else {
			list := c.get("list", "List "+resource.name, resource.path, 0)
			list.Flags().Int("limit", 100, "Maximum page size")
			list.Flags().String("cursor", "", "Continuation cursor")
			if resource.name == "task" {
				for _, name := range []string{"host", "status", "type"} {
					list.Flags().String(name, "", "Filter by "+name)
				}
			}
			group.AddCommand(list)
			if resource.show {
				group.AddCommand(c.get("show ID", "Show record", resource.path, 1))
			}
		}
		root.AddCommand(group)
	}
	child(root, "credential").AddCommand(c.get("recipients", "List credential recipient metadata", "/credentials/recipients", 0))
	rejection := &cobra.Command{Use: "rejection", Short: "Manage enrollment rejections"}
	rejection.AddCommand(c.get("list", "List rejections", "/enrollment/rejections", 0))
	child(root, "enrollment").AddCommand(rejection)
	for _, kind := range []string{"host", "task"} {
		logs := &cobra.Command{
			Use:   "logs ID",
			Short: "Read retained logs",
			Args:  cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				path := "/" + kind + "s/" + url.PathEscape(args[0]) + "/logs"
				if boolean(cmd, "follow") {
					return c.watch(cmd, path+"/stream")
				}
				return c.call(cmd, http.MethodGet, path, nil)
			},
		}
		logs.Flags().Bool("follow", false, "Replay retained logs and follow new entries")
		child(root, kind).AddCommand(logs)
	}
	child(root, "host").AddCommand(&cobra.Command{
		Use:   "disks ID",
		Short: "List host disks",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return c.call(cmd, http.MethodGet, "/hosts/"+url.PathEscape(args[0])+"/disks", nil)
		},
	})
	metrics := &cobra.Command{
		Use:   "metrics ID",
		Short: "Follow live host metrics",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return c.watch(cmd, "/hosts/"+url.PathEscape(args[0])+"/metrics/stream")
		},
	}
	metrics.Flags().Bool("follow", true, "Follow live host metrics")
	child(root, "host").AddCommand(metrics)
	child(root, "task").AddCommand(&cobra.Command{
		Use:   "watch ID",
		Short: "Follow task state",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return c.watch(cmd, "/tasks/"+url.PathEscape(args[0])+"/events")
		},
	})
	child(root, "task").AddCommand(&cobra.Command{
		Use:   "output ID OUTPUT_ID",
		Short: "Read a structured task output",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return c.call(cmd, http.MethodGet, "/tasks/"+url.PathEscape(args[0])+"/outputs/"+url.PathEscape(args[1]), nil)
		},
	})
	for _, group := range []string{"recipe", "configuration"} {
		show := &cobra.Command{
			Use:   "show ID",
			Short: "Read an immutable " + group + " version",
			Args:  cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				path, err := versionPath(cmd, group, args[0])
				if err != nil {
					return err
				}
				return c.call(cmd, http.MethodGet, path, nil)
			},
		}
		versionFlag(show)
		child(root, group).AddCommand(show)
	}

	network := &cobra.Command{Use: "network", Short: "Manage local networking"}
	wifi := &cobra.Command{Use: "wifi", Short: "Manage Wi-Fi"}
	scan := c.get("list", "Scan Wi-Fi networks", "/network/wifi", 0)
	scan.Flags().String("interface", "", "Network interface")
	wifi.AddCommand(scan)
	network.AddCommand(wifi)
	root.AddCommand(network)
}

// child is used only when assembling known Cobra command groups, never as a
// second dispatch mechanism at runtime.
func child(parent *cobra.Command, name string) *cobra.Command {
	for _, command := range parent.Commands() {
		if command.Name() == name {
			return command
		}
	}
	panic("missing command group: " + name)
}
