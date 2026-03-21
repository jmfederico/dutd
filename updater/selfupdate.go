package updater

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
)

const (
	// labelPredecessor is set on a successor dutd container to identify the
	// old container it should clean up on startup.
	labelPredecessor = "io.dutd.predecessor"

	// labelPredecessorName is set on a successor dutd container so it knows
	// what name to rename itself to after removing the predecessor.
	labelPredecessorName = "io.dutd.predecessor.name"

	// selfUpdateSuffix is appended to the container name when creating the
	// successor during a self-update. The successor renames itself back to
	// the original name after removing the predecessor.
	selfUpdateSuffix = "-dutd-next"
)

// ErrSelfUpdateRestart is returned by updateContainer when dutd has launched
// a successor and the caller should exit so the new instance takes over.
var ErrSelfUpdateRestart = errors.New("self-update: successor started, shutting down")

// DetectSelfContainerID returns the container ID of the running dutd instance
// by reading the HOSTNAME environment variable, which Docker sets to the
// container ID by default. Returns empty string when not running in a container
// (e.g. during development or testing).
func DetectSelfContainerID() string {
	hostname := os.Getenv("HOSTNAME")
	// Docker container IDs are 64 hex characters; HOSTNAME is typically the
	// first 12. If it looks like a hex string of 12+ chars, treat it as a
	// container ID. Otherwise we're probably not in a container.
	if len(hostname) >= 12 && isHex(hostname) {
		return hostname
	}
	return ""
}

// isHex reports whether s consists entirely of hexadecimal characters.
func isHex(s string) bool {
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return len(s) > 0
}

// CleanupPredecessor checks if this dutd instance was started as a successor
// during a self-update. If so, it removes the old (stopped) predecessor
// container and renames itself to the original name.
//
// This should be called once at startup, before the update loop begins.
func CleanupPredecessor(ctx context.Context, cli DockerClient, selfID string, log *slog.Logger) error {
	if selfID == "" {
		return nil
	}

	info, err := cli.ContainerInspect(ctx, selfID)
	if err != nil {
		return fmt.Errorf("inspect self (%s): %w", selfID, err)
	}

	predecessorID := info.Config.Labels[labelPredecessor]
	predecessorName := info.Config.Labels[labelPredecessorName]

	if predecessorID == "" || predecessorName == "" {
		return nil // not a successor — nothing to clean up
	}

	log.Info("self-update cleanup: removing predecessor",
		"predecessor_id", shortID(predecessorID),
		"predecessor_name", predecessorName,
	)

	// The predecessor should already be stopped (it exited after starting us),
	// but stop it just in case with a short timeout.
	timeout := 5
	_ = cli.ContainerStop(ctx, predecessorID, container.StopOptions{Timeout: &timeout})

	if err := cli.ContainerRemove(ctx, predecessorID, container.RemoveOptions{Force: true}); err != nil {
		// Non-fatal: the predecessor may have already been removed.
		log.Warn("could not remove predecessor container",
			"predecessor_id", shortID(predecessorID),
			"err", err,
		)
	}

	// Rename ourselves from "<name>-dutd-next" back to "<name>".
	selfName := strings.TrimPrefix(info.Name, "/")
	if selfName != predecessorName {
		log.Info("self-update cleanup: renaming self",
			"from", selfName,
			"to", predecessorName,
		)
		if err := cli.ContainerRename(ctx, selfID, predecessorName); err != nil {
			return fmt.Errorf("rename self from %q to %q: %w", selfName, predecessorName, err)
		}
	}

	log.Info("self-update cleanup complete", "name", predecessorName)
	return nil
}

// selfUpdate performs a self-update by creating a successor container with the
// new image and starting it. The caller is expected to exit after this returns
// so the successor can take over.
//
// The sequence is:
//  1. Snapshot dutd's own container config
//  2. Create a new container with name "<name>-dutd-next" using the new image,
//     with labels pointing back to the old container for cleanup
//  3. Start the successor
//  4. Return ErrSelfUpdateRestart — the caller exits, and the successor
//     calls CleanupPredecessor on startup to remove the old container and
//     rename itself
func (u *Updater) selfUpdate(ctx context.Context, ct container.Summary, newImage string) error {
	name := containerName(ct)

	u.log.Info("self-update: starting", "name", name, "image", newImage)

	// Snapshot our own config so the successor is an exact replica.
	info, err := snapshotContainer(ctx, u.cli, ct.ID)
	if err != nil {
		return fmt.Errorf("self-update snapshot: %w", err)
	}

	// Prepare the successor's config.
	cfg := info.Config
	cfg.Image = newImage

	// Add labels so the successor knows which container to clean up.
	if cfg.Labels == nil {
		cfg.Labels = make(map[string]string)
	}
	cfg.Labels[labelPredecessor] = ct.ID
	cfg.Labels[labelPredecessorName] = name

	hc := info.HostConfig

	// Build networking config (same logic as recreate).
	netCfg := &network.NetworkingConfig{
		EndpointsConfig: make(map[string]*network.EndpointSettings),
	}

	var extraNetworks []string
	first := true
	for netName, ep := range info.NetworkSettings.Networks {
		if first {
			netCfg.EndpointsConfig[netName] = &network.EndpointSettings{
				Aliases:   ep.Aliases,
				IPAddress: ep.IPAddress,
			}
			first = false
		} else {
			extraNetworks = append(extraNetworks, netName)
		}
	}

	successorName := name + selfUpdateSuffix

	u.log.Info("self-update: creating successor", "name", successorName, "image", newImage)
	resp, err := u.cli.ContainerCreate(ctx, cfg, hc, netCfg, nil, successorName)
	if err != nil {
		return fmt.Errorf("self-update create successor: %w", err)
	}

	// Connect extra networks before starting.
	for _, netName := range extraNetworks {
		ep := info.NetworkSettings.Networks[netName]
		if err := u.cli.NetworkConnect(ctx, netName, resp.ID, &network.EndpointSettings{
			Aliases:   ep.Aliases,
			IPAddress: ep.IPAddress,
		}); err != nil {
			u.log.Warn("self-update: could not reconnect network",
				"network", netName,
				"container", successorName,
				"err", err,
			)
		}
	}

	u.log.Info("self-update: starting successor", "name", successorName, "id", shortID(resp.ID))
	if err := u.cli.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		return fmt.Errorf("self-update start successor: %w", err)
	}

	u.log.Info("self-update: successor started, this instance will now shut down",
		"successor_name", successorName,
		"successor_id", shortID(resp.ID),
	)

	return ErrSelfUpdateRestart
}

// shortID returns the first 12 characters of an ID for compact logging.
func shortID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}
