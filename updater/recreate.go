package updater

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
)

// containerInfo is a lightweight snapshot of what we need to recreate a container.
type containerInfo struct {
	Name            string
	Config          *container.Config
	HostConfig      *container.HostConfig
	NetworkSettings networkSnapshot
}

type networkSnapshot struct {
	Networks map[string]*networkEndpoint
}

type networkEndpoint struct {
	Aliases   []string
	IPAddress string
}

// snapshotContainer captures the container configuration required for recreation.
func snapshotContainer(ctx context.Context, cli DockerClient, id string) (containerInfo, error) {
	resp, err := cli.ContainerInspect(ctx, id)
	if err != nil {
		return containerInfo{}, fmt.Errorf("inspect %s: %w", id[:12], err)
	}

	name := strings.TrimPrefix(resp.Name, "/")

	snap := containerInfo{
		Name:       name,
		Config:     resp.Config,
		HostConfig: resp.HostConfig,
		NetworkSettings: networkSnapshot{
			Networks: make(map[string]*networkEndpoint),
		},
	}

	if resp.NetworkSettings != nil {
		for netName, ep := range resp.NetworkSettings.Networks {
			snap.NetworkSettings.Networks[netName] = &networkEndpoint{
				Aliases:   ep.Aliases,
				IPAddress: ep.IPAddress,
			}
		}
	}

	return snap, nil
}

// pullImage pulls the given image reference, draining the progress stream.
func pullImage(ctx context.Context, cli DockerClient, imageRef string, log *slog.Logger) error {
	log.Info("pulling image", "image", imageRef)

	rc, err := cli.ImagePull(ctx, imageRef, image.PullOptions{})
	if err != nil {
		return fmt.Errorf("pull %s: %w", imageRef, err)
	}
	defer rc.Close()

	if _, err := io.Copy(io.Discard, rc); err != nil {
		return fmt.Errorf("drain pull response for %s: %w", imageRef, err)
	}

	return nil
}

// imageID returns the content-addressable ID (sha256:...) of a locally stored
// image matching the given reference. This is the same format as
// container.Summary.ImageID, making the two directly comparable.
// Returns empty string if the image is not found locally.
func imageID(ctx context.Context, cli DockerClient, imageRef string) (string, error) {
	resp, _, err := cli.ImageInspectWithRaw(ctx, imageRef)
	if err != nil {
		return "", fmt.Errorf("inspect image %s: %w", imageRef, err)
	}

	return resp.ID, nil
}

// stopAndRemove gracefully stops then removes a container.
// Docker sends SIGTERM and waits stopTimeout seconds before sending SIGKILL.
func stopAndRemove(ctx context.Context, cli DockerClient, id string, stopTimeout int, log *slog.Logger) error {
	log.Info("stopping container", "id", id[:12], "stop_timeout_sec", stopTimeout)

	t := stopTimeout
	if err := cli.ContainerStop(ctx, id, container.StopOptions{Timeout: &t}); err != nil {
		return fmt.Errorf("stop container %s: %w", id[:12], err)
	}

	log.Info("removing container", "id", id[:12])
	if err := cli.ContainerRemove(ctx, id, container.RemoveOptions{Force: false}); err != nil {
		return fmt.Errorf("remove container %s: %w", id[:12], err)
	}

	return nil
}

// recreate creates and starts a new container that is an exact replica of the
// snapshotted container using newImage. Preserved configuration:
//   - Name, Env, Cmd, Entrypoint, WorkingDir, User, Labels
//   - ExposedPorts + PortBindings, Volume mounts/binds
//   - RestartPolicy, NetworkMode, SecurityOpt, CapAdd/CapDrop
//   - All network endpoints (aliases reconnected after start)
func recreate(ctx context.Context, cli DockerClient, info containerInfo, newImage string, log *slog.Logger) error {
	cfg := info.Config
	cfg.Image = newImage

	hc := info.HostConfig

	// Docker only allows one network at container-creation time.
	// Seed with the first network; connect extras after the container is created.
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

	log.Info("creating container", "name", info.Name, "image", newImage)
	resp, err := cli.ContainerCreate(ctx, cfg, hc, netCfg, nil, info.Name)
	if err != nil {
		return fmt.Errorf("create container %s: %w", info.Name, err)
	}

	// Connect extra networks before starting.
	for _, netName := range extraNetworks {
		ep := info.NetworkSettings.Networks[netName]
		if err := cli.NetworkConnect(ctx, netName, resp.ID, &network.EndpointSettings{
			Aliases:   ep.Aliases,
			IPAddress: ep.IPAddress,
		}); err != nil {
			// Non-fatal: container will start without this network.
			log.Warn("could not reconnect network", "network", netName, "container", info.Name, "err", err)
		}
	}

	log.Info("starting container", "name", info.Name, "id", resp.ID[:12])
	if err := cli.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		return fmt.Errorf("start container %s: %w", info.Name, err)
	}

	return nil
}

// shortDigest returns the first 12 hex chars of a digest for compact logging.
func shortDigest(d string) string {
	d = strings.TrimPrefix(d, "sha256:")
	if len(d) > 12 {
		return d[:12]
	}
	return d
}

// retryWithBackoff retries f up to maxAttempts times with linear backoff.
func retryWithBackoff(maxAttempts int, delay time.Duration, f func() error) error {
	var lastErr error
	for i := range maxAttempts {
		lastErr = f()
		if lastErr == nil {
			return nil
		}
		if i < maxAttempts-1 {
			time.Sleep(delay * time.Duration(i+1))
		}
	}
	return lastErr
}
