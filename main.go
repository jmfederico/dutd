package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	dockerclient "github.com/jmfederico/dutd/docker"
	"github.com/jmfederico/dutd/updater"
)

// multiFlag allows a CLI flag to be specified multiple times.
// e.g.  --name "web-*" --name "api-*"
type multiFlag []string

func (m *multiFlag) String() string {
	if m == nil {
		return ""
	}
	s := ""
	for i, v := range *m {
		if i > 0 {
			s += ", "
		}
		s += v
	}
	return s
}

func (m *multiFlag) Set(v string) error {
	*m = append(*m, v)
	return nil
}

func main() {
	var (
		socket      = flag.String("socket", "/var/run/docker.sock", "Path to the Docker Unix socket")
		intervalStr = flag.String("interval", "1h", "How often to check for updates (e.g. 30m, 2h, 24h)")
		stopTimeout = flag.Int("stop-timeout", 30, "Seconds to wait for a container to stop gracefully before SIGKILL")
		nameGlobs   multiFlag
		tags        multiFlag
	)

	flag.Var(&nameGlobs, "name", "Glob pattern to match container names (repeatable, e.g. --name \"web-*\")")
	flag.Var(&tags, "tag", "Exact image tag to match (repeatable, e.g. --tag nginx:latest)")

	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, `dutd — Docker Up To Date

Periodically pulls the latest version of images for running containers and
recreates them when the digest changes. Filters are additive (union): a
container is updated if it matches any --name glob OR any --tag value.

Usage:
  dutd [flags]

Flags:
`)
		flag.PrintDefaults()
		fmt.Fprintf(os.Stderr, `
Examples:
  # Update all containers tagged :latest every hour
  dutd --tag nginx:latest --tag redis:latest

  # Update all containers whose name starts with "web-" or "api-" every 30 minutes
  dutd --interval 30m --name "web-*" --name "api-*"

  # Update every container on the host every 6 hours
  dutd --interval 6h --name "*"
`)
	}

	flag.Parse()

	// Validate that at least one filter was provided.
	if len(nameGlobs) == 0 && len(tags) == 0 {
		fmt.Fprintln(os.Stderr, "error: at least one --name or --tag filter is required")
		flag.Usage()
		os.Exit(1)
	}

	interval, err := time.ParseDuration(*intervalStr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: invalid --interval %q: %v\n", *intervalStr, err)
		os.Exit(1)
	}
	if interval <= 0 {
		fmt.Fprintln(os.Stderr, "error: --interval must be a positive duration")
		os.Exit(1)
	}

	// Structured JSON logger to stdout.
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))

	// Connect to Docker via Unix socket only.
	cli, err := dockerclient.NewSocketClient(*socket)
	if err != nil {
		log.Error("failed to create docker client", "socket", *socket, "err", err)
		os.Exit(1)
	}
	defer cli.Close()

	// Verify connectivity early so the user gets a clear error on startup.
	ctx := context.Background()
	if _, err := cli.Ping(ctx); err != nil {
		log.Error("docker socket not reachable", "socket", *socket, "err", err)
		os.Exit(1)
	}

	// Detect if we're running inside a container so we can self-update.
	selfID := updater.DetectSelfContainerID()
	if selfID != "" {
		log.Info("running in container, self-update enabled", "self_id", selfID)
	}

	// If this instance was started as a successor during a self-update,
	// clean up the predecessor container and rename ourselves.
	if err := updater.CleanupPredecessor(ctx, cli, selfID, log); err != nil {
		log.Error("predecessor cleanup failed", "err", err)
		// Non-fatal: continue running even if cleanup fails.
	}

	cfg := &updater.Config{
		NameGlobs: []string(nameGlobs),
		Tags:      []string(tags),
	}

	u := updater.New(cli, cfg, interval, *stopTimeout, selfID, log)

	// Run until SIGINT or SIGTERM.
	runCtx, cancel := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	u.Run(runCtx)
}
