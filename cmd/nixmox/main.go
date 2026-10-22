package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"

	"nixmox/internal/client"
	"nixmox/internal/cluster"
	"nixmox/internal/config"
	"nixmox/internal/domain"
	"nixmox/internal/httpapi"
	"nixmox/internal/machine"
	"nixmox/internal/network"
	"nixmox/internal/presentation"
	"nixmox/internal/service"
	"nixmox/internal/storage"
	"nixmox/internal/transport"
	"nixmox/internal/tui"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	os.Exit(run(ctx, os.Args[1:], os.Stdout, os.Stderr))
}
func flags(name string, errOut io.Writer) *flag.FlagSet {
	f := flag.NewFlagSet(name, flag.ContinueOnError)
	f.SetOutput(errOut)
	return f
}
func printJSON(out io.Writer, v any) {
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}
func exitError(out io.Writer, err error) int {
	fmt.Fprintln(out, err)
	var e *domain.Error
	if errors.As(err, &e) {
		if e.Status == 503 {
			return 3
		}
		if e.Status >= 400 && e.Status < 500 {
			return 2
		}
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return 3
	}
	return 1
}
func run(ctx context.Context, args []string, out, errOut io.Writer) int {
	root := flags("nixmox", errOut)
	endpoint := root.String("endpoint", "", "HTTP console origin")
	configPath := root.String("config", "", "runtime YAML path")
	if err := root.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	args = root.Args()
	if len(args) == 0 {
		fmt.Fprintln(out, "Usage: nixmox [--endpoint URL] {serve|identity show|network status|network check|tasks list|tasks show|tasks cancel|tasks retry|commands show|tui}")
		return 2
	}
	if args[0] == "serve" {
		return serve(ctx, args[1:], *configPath, out, errOut)
	}
	ep := *endpoint
	if ep == "" {
		ep = os.Getenv("NIXMOX_ENDPOINT")
	}
	if ep == "" {
		path := *configPath
		if path == "" {
			if _, err := os.Stat("/etc/nixmox/config.yaml"); err == nil {
				path = "/etc/nixmox/config.yaml"
			}
		}
		cfg, err := config.Load(path)
		if err != nil {
			fmt.Fprintln(errOut, err)
			return 2
		}
		ep = "http://127.0.0.1:" + strconv.Itoa(cfg.HTTP.Port)
	}
	c, err := client.New(ep)
	if err != nil {
		return exitError(errOut, err)
	}
	if args[0] == "tui" {
		if len(args) != 1 {
			fmt.Fprintln(errOut, "tui takes no options in this build")
			return 2
		}
		return errorCode(errOut, tui.Run(ctx, c))
	}
	if len(args) < 2 {
		fmt.Fprintln(errOut, "A subcommand is required.")
		return 2
	}
	group, verb := args[0], args[1]
	rest := args[2:]
	clusterName := ""
	if group == "cluster" && verb == "create" {
		if len(rest) == 0 {
			fmt.Fprintln(errOut, "A cluster name is required.")
			return 2
		}
		clusterName = rest[0]
		rest = rest[1:]
	}
	id := ""
	if (group == "tasks" && verb != "list") || group == "commands" {
		if len(rest) == 0 {
			fmt.Fprintln(errOut, "An ID is required.")
			return 2
		}
		id = rest[0]
		rest = rest[1:]
		if !domain.SafeID.MatchString(id) {
			fmt.Fprintln(errOut, "Invalid ID.")
			return 2
		}
	}
	f := flags(group+" "+verb, errOut)
	asJSON := f.Bool("json", false, "JSON output")
	mutation := (group == "network" && verb == "check") || (group == "tasks" && (verb == "cancel" || verb == "retry")) || (group == "cluster" && verb == "create")
	var wait bool
	var commandID string
	var version int64
	if mutation {
		f.BoolVar(&wait, "wait", false, "wait for completion")
		f.StringVar(&commandID, "command-id", "", "stable retry ID")
		if group == "tasks" {
			f.Int64Var(&version, "version", 0, "reviewed task version")
			f.Int64Var(&version, "expected-version", 0, "reviewed task version")
		}
	}
	filters := map[string]*string{}
	if group == "tasks" && verb == "list" {
		for _, key := range []string{"host", "kind", "state", "since", "until", "cursor", "limit"} {
			filters[key] = f.String(key, "", "task filter")
		}
	}
	if err := f.Parse(rest); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if f.NArg() != 0 {
		fmt.Fprintln(errOut, "Unexpected arguments.")
		return 2
	}
	if mutation {
		identity, err := c.Identity(ctx)
		if err != nil {
			return exitError(errOut, err)
		}
		if commandID == "" {
			commandID = domain.NewID("cmd_")
		}
		cmd := domain.Command{ID: commandID, Type: group + "." + verb, TargetID: identity.NodeID, ExpectedVersions: map[string]int64{}, Payload: json.RawMessage(`{}`)}
		if group == "cluster" {
			cmd.Payload, _ = json.Marshal(map[string]string{"name": clusterName})
		}
		if group == "tasks" {
			if version <= 0 {
				fmt.Fprintln(errOut, "--version must be the reviewed task version.")
				return 2
			}
			cmd.Type = "task." + verb
			cmd.ExpectedVersions["task:"+id] = version
			cmd.Payload, _ = json.Marshal(map[string]string{"task_id": id})
		}
		fmt.Fprintln(errOut, "Command ID:", cmd.ID)
		receipt, err := c.Submit(ctx, cmd)
		if err != nil {
			return exitError(errOut, err)
		}
		if !wait {
			if *asJSON {
				printJSON(out, receipt)
			} else {
				fmt.Fprintf(out, "Accepted %s · task %s\n", receipt.CommandID, receipt.TaskID)
			}
			return 0
		}
		task, err := c.Wait(ctx, receipt.TaskID)
		if err != nil {
			return exitError(errOut, err)
		}
		if *asJSON {
			printJSON(out, task)
		} else {
			fmt.Fprintln(out, presentation.Task(task))
			if result := task.NetworkResult(); result != nil {
				fmt.Fprintln(out, presentation.Network(*result))
			}
		}
		if task.State == "blocked" {
			return 4
		}
		if task.State != "succeeded" {
			return 1
		}
		return 0
	}
	switch group + " " + verb {
	case "cluster status":
		v, err := c.Cluster(ctx)
		if err != nil {
			return exitError(errOut, err)
		}
		printJSON(out, v)
	case "identity show":
		v, err := c.Identity(ctx)
		if err != nil {
			return exitError(errOut, err)
		}
		printJSON(out, v)
	case "network status":
		v, err := c.Network(ctx)
		if err != nil {
			return exitError(errOut, err)
		}
		if *asJSON {
			printJSON(out, v)
		} else {
			fmt.Fprintln(out, presentation.Network(v))
		}
	case "tasks show":
		v, err := c.Task(ctx, id)
		if err != nil {
			return exitError(errOut, err)
		}
		if *asJSON {
			printJSON(out, v)
		} else {
			fmt.Fprintln(out, presentation.Task(v))
		}
	case "tasks list":
		q := url.Values{}
		for k, v := range filters {
			if *v != "" {
				if k == "host" {
					k = "host_id"
				}
				q.Set(k, *v)
			}
		}
		p, err := c.Tasks(ctx, "?"+q.Encode())
		if err != nil {
			return exitError(errOut, err)
		}
		if *asJSON {
			printJSON(out, p)
		} else {
			for _, t := range p.Items {
				fmt.Fprintln(out, presentation.Task(t))
			}
			if p.NextCursor != nil {
				fmt.Fprintln(out, "Next cursor:", *p.NextCursor)
			}
		}
	case "commands show":
		var v domain.Receipt
		if err := c.Read(ctx, "/api/v1/commands/"+id, &v); err != nil {
			return exitError(errOut, err)
		}
		printJSON(out, v)
	case "tasks logs":
		var v any
		if err := c.Read(ctx, "/api/v1/tasks/"+id+"/logs", &v); err != nil {
			return exitError(errOut, err)
		}
		if *asJSON {
			printJSON(out, v)
		}
	default:
		fmt.Fprintln(errOut, "Unsupported command.")
		return 2
	}
	return 0
}
func errorCode(out io.Writer, err error) int {
	if err != nil {
		return exitError(out, err)
	}
	return 0
}
func serve(ctx context.Context, args []string, inherited string, out, errOut io.Writer) int {
	f := flags("serve", errOut)
	path := f.String("config", inherited, "runtime YAML path")
	data := f.String("data-dir", "", "private state directory override")
	port := f.Int("http-port", 0, "console port override")
	if err := f.Parse(args); err != nil {
		return 2
	}
	if f.NArg() != 0 {
		fmt.Fprintln(errOut, "Unexpected arguments.")
		return 2
	}
	cfg, err := config.Load(*path)
	if err != nil {
		return exitError(errOut, err)
	}
	f.Visit(func(v *flag.Flag) {
		switch v.Name {
		case "data-dir":
			cfg.DataDir = *data
		case "http-port":
			cfg.HTTP.Port = *port
		}
	})
	if err := cfg.Validate(); err != nil {
		return exitError(errOut, err)
	}
	store, err := storage.Open(cfg.DataDir)
	if err != nil {
		return exitError(errOut, err)
	}
	defer store.Close()
	clusters, err := cluster.Open(store, cfg)
	if err != nil {
		return exitError(errOut, err)
	}
	defer clusters.Close()
	svc, err := service.Open(store, network.Inspector{Config: cfg}, clusters.Operations()...)
	if err != nil {
		return exitError(errOut, err)
	}
	defer svc.Close()
	addresses, err := network.BindAddresses(cfg)
	if err != nil {
		return exitError(errOut, err)
	}
	listeners := []net.Listener{}
	defer func() {
		for _, l := range listeners {
			_ = l.Close()
		}
	}()
	for _, address := range addresses {
		l, err := net.Listen("tcp", address)
		if err != nil {
			return exitError(errOut, fmt.Errorf("http listener %s (http.port): %w", address, err))
		}
		listeners = append(listeners, l)
	}
	handler := httpapi.New(svc, clusters)
	servers := []*http.Server{}
	handlers := make([]http.Handler, len(listeners))
	for i := range handlers {
		handlers[i] = handler
	}
	forwarders := []*transport.Forward{}
	defer func() {
		for _, f := range forwarders {
			f.Close()
		}
	}()
	for _, address := range addresses {
		host, _, _ := net.SplitHostPort(address)
		machineAddress := net.JoinHostPort(host, strconv.Itoa(cfg.Machine.Port))
		l, err := net.Listen("tcp", machineAddress)
		if err != nil {
			return exitError(errOut, fmt.Errorf("machine listener %s (machine.port): %w", machineAddress, err))
		}
		listeners = append(listeners, tls.NewListener(l, clusters.TLS()))
		handlers = append(handlers, machine.New(nil))
		if host == "127.0.0.1" {
			continue
		}
		for _, port := range []int{cfg.NATS.RoutePort, cfg.NATS.LeafPort} {
			address := net.JoinHostPort(host, strconv.Itoa(port))
			f, err := transport.ListenForward(address, net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
			if err != nil {
				return exitError(errOut, fmt.Errorf("broker listener %s (nats ports): %w", address, err))
			}
			forwarders = append(forwarders, f)
		}
	}
	failures := make(chan error, len(listeners))
	for i, l := range listeners {
		server := &http.Server{Handler: handlers[i], ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 * 1024}
		servers = append(servers, server)
		go func() { failures <- server.Serve(l) }()
	}
	fmt.Fprintf(out, "Nixmox ready: node %s, console %v\n", svc.Identity().NodeID, addresses)
	var serveErr error
	select {
	case <-ctx.Done():
	case serveErr = <-failures:
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	for _, server := range servers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := server.Shutdown(shutdown); err != nil {
				_ = server.Close()
			}
		}()
	}
	wg.Wait()
	if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
		return exitError(errOut, serveErr)
	}
	return 0
}
