package updater

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync/atomic"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
)

// Updater is the long-running daemon that periodically checks running containers
// against the configured filter criteria and updates them when a newer image is available.
type Updater struct {
	cli         DockerClient
	cfg         *Config
	interval    time.Duration
	stopTimeout int
	log         *slog.Logger

	// selfID is the container ID of this dutd instance. When set, the updater
	// will use the self-update path instead of the normal stop/remove/recreate
	// cycle for its own container. Empty when not running in a container.
	selfID string

	// retryDelay is the base delay between pull retries. Defaults to 5s.
	retryDelay time.Duration

	// running is 1 while a check is in progress. Ticks that arrive while a
	// check is already running are dropped rather than queued.
	running atomic.Bool
}

// New creates a new Updater.
//
//   - cli          – Docker client connected via Unix socket
//   - cfg          – filter criteria (name globs + exact tags)
//   - interval     – how often to run the update check
//   - stopTimeout  – seconds to wait for container shutdown before SIGKILL
//   - selfID       – container ID of this dutd instance (empty if not in a container)
//   - log          – structured JSON logger
func New(cli DockerClient, cfg *Config, interval time.Duration, stopTimeout int, selfID string, log *slog.Logger) *Updater {
	return &Updater{
		cli:         cli,
		cfg:         cfg,
		interval:    interval,
		stopTimeout: stopTimeout,
		selfID:      selfID,
		retryDelay:  5 * time.Second,
		log:         log,
	}
}

// Run starts the update loop. It blocks until ctx is cancelled or a
// self-update is triggered (in which case a successor has been started and
// this instance should exit).
// The first check runs immediately on startup, then repeats every interval.
func (u *Updater) Run(ctx context.Context) {
	u.log.Info("dutd started",
		"interval", u.interval,
		"stop_timeout_sec", u.stopTimeout,
		"self_id", u.selfID,
	)

	if u.runOnce(ctx) {
		return // self-update triggered
	}

	ticker := time.NewTicker(u.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			u.log.Info("dutd shutting down")
			return
		case <-ticker.C:
			if u.running.Load() {
				u.log.Info("previous check still running, skipping tick")
				continue
			}
			if u.runOnce(ctx) {
				return // self-update triggered
			}
		}
	}
}

// runOnce performs a single pass over all running containers.
// It sets the running flag for its duration so that concurrent ticks are skipped.
// Returns true if a self-update was triggered and the caller should exit.
func (u *Updater) runOnce(ctx context.Context) bool {
	u.running.Store(true)
	defer u.running.Store(false)

	u.log.Info("starting update check")

	containers, err := u.listRunning(ctx)
	if err != nil {
		u.log.Error("failed to list containers", "err", err)
		return false
	}

	u.log.Info("containers found", "total", len(containers))

	var checked, updated, skipped, failed int
	var selfContainer *container.Summary

	for _, ct := range containers {
		name := containerName(ct)

		if !u.cfg.Matches(ct) {
			continue
		}

		// If this is our own container, defer it until after all other
		// containers have been updated. Self-update is always last because
		// it will cause this instance to exit.
		if u.isSelf(ct) {
			ct := ct // capture loop variable
			selfContainer = &ct
			u.log.Info("deferring self-update to end of cycle", "name", name)
			continue
		}

		checked++
		u.log.Info("checking container", "name", name, "image", ct.Image)

		didUpdate, err := u.updateContainer(ctx, ct)
		if err != nil {
			u.log.Error("update failed", "name", name, "image", ct.Image, "err", err)
			failed++
			continue
		}

		if didUpdate {
			updated++
		} else {
			skipped++
		}
	}

	u.log.Info("update check complete",
		"checked", checked,
		"updated", updated,
		"already_current", skipped,
		"failed", failed,
	)

	// Handle self-update last — after all other containers are done.
	if selfContainer != nil {
		selfRestarted, err := u.updateSelf(ctx, *selfContainer)
		if err != nil {
			u.log.Error("self-update failed", "err", err)
			return false
		}
		return selfRestarted
	}

	return false
}

// isSelf reports whether the given container is this dutd instance.
func (u *Updater) isSelf(ct container.Summary) bool {
	if u.selfID == "" {
		return false
	}
	// ct.ID is the full 64-char hex ID; u.selfID may be the first 12 chars
	// (from HOSTNAME). Match by prefix.
	return strings.HasPrefix(ct.ID, u.selfID) || strings.HasPrefix(u.selfID, ct.ID)
}

// updateContainer pulls the image for a single container and, if the digest
// has changed, stops the old container and recreates it with the new image.
// Returns true if the container was actually restarted.
func (u *Updater) updateContainer(ctx context.Context, ct container.Summary) (bool, error) {
	name := containerName(ct)

	// Record the currently running image ID (sha256:...) before pulling.
	oldID := ct.ImageID

	// Pull the latest version (up to 3 attempts with linear backoff).
	err := retryWithBackoff(3, u.retryDelay, func() error {
		return pullImage(ctx, u.cli, ct.Image, u.log)
	})
	if err != nil {
		return false, fmt.Errorf("pull image: %w", err)
	}

	// Resolve the image ID of the tag we just pulled. This returns the same
	// sha256:... format as ct.ImageID, so the two are directly comparable.
	newID, err := imageID(ctx, u.cli, ct.Image)
	if err != nil {
		return false, fmt.Errorf("resolve new image id: %w", err)
	}

	// If the image ID is unchanged, the pull fetched the same image — skip.
	if newID != "" && oldID != "" && newID == oldID {
		u.log.Info("already up to date",
			"name", name,
			"image", ct.Image,
			"image_id", shortDigest(oldID),
		)
		return false, nil
	}

	u.log.Info("image updated, recreating container",
		"name", name,
		"image", ct.Image,
		"old_id", shortDigest(oldID),
		"new_id", shortDigest(newID),
	)

	// Snapshot the full container config before stopping it.
	info, err := snapshotContainer(ctx, u.cli, ct.ID)
	if err != nil {
		return false, fmt.Errorf("snapshot container config: %w", err)
	}

	// Stop and remove the old container.
	if err := stopAndRemove(ctx, u.cli, ct.ID, u.stopTimeout, u.log); err != nil {
		return false, fmt.Errorf("stop/remove container: %w", err)
	}

	// Recreate with the new image.
	if err := recreate(ctx, u.cli, info, ct.Image, u.log); err != nil {
		return false, fmt.Errorf("recreate container: %w", err)
	}

	u.log.Info("container updated successfully", "name", name, "image", ct.Image)
	return true, nil
}

// updateSelf handles the special self-update path for dutd's own container.
// It pulls the new image, checks if it changed, and if so, creates a successor
// container and signals the caller to exit. Returns true if a self-update was
// triggered and the process should exit.
func (u *Updater) updateSelf(ctx context.Context, ct container.Summary) (bool, error) {
	name := containerName(ct)
	oldID := ct.ImageID

	u.log.Info("checking self for updates", "name", name, "image", ct.Image)

	// Pull the latest version.
	err := retryWithBackoff(3, u.retryDelay, func() error {
		return pullImage(ctx, u.cli, ct.Image, u.log)
	})
	if err != nil {
		return false, fmt.Errorf("self-update pull image: %w", err)
	}

	newID, err := imageID(ctx, u.cli, ct.Image)
	if err != nil {
		return false, fmt.Errorf("self-update resolve image id: %w", err)
	}

	if newID != "" && oldID != "" && newID == oldID {
		u.log.Info("self already up to date",
			"name", name,
			"image", ct.Image,
			"image_id", shortDigest(oldID),
		)
		return false, nil
	}

	u.log.Info("self image updated, triggering self-update",
		"name", name,
		"image", ct.Image,
		"old_id", shortDigest(oldID),
		"new_id", shortDigest(newID),
	)

	if err := u.selfUpdate(ctx, ct, ct.Image); err != nil {
		if errors.Is(err, ErrSelfUpdateRestart) {
			return true, nil
		}
		return false, err
	}

	return false, nil
}

// listRunning returns all currently running containers.
func (u *Updater) listRunning(ctx context.Context) ([]container.Summary, error) {
	f := filters.NewArgs(filters.Arg("status", "running"))
	return u.cli.ContainerList(ctx, container.ListOptions{Filters: f})
}
