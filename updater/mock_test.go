package updater

import (
	"context"
	"io"
	"log/slog"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// mockClient is a hand-written mock for DockerClient. Each method delegates to
// the corresponding function field, which can be set per-test. Unset fields
// panic with a clear message so that missing expectations are immediately visible.
type mockClient struct {
	containerListFn       func(ctx context.Context, options container.ListOptions) ([]container.Summary, error)
	containerInspectFn    func(ctx context.Context, containerID string) (container.InspectResponse, error)
	containerStopFn       func(ctx context.Context, containerID string, options container.StopOptions) error
	containerRemoveFn     func(ctx context.Context, containerID string, options container.RemoveOptions) error
	containerCreateFn     func(ctx context.Context, config *container.Config, hostConfig *container.HostConfig, networkingConfig *network.NetworkingConfig, platform *ocispec.Platform, containerName string) (container.CreateResponse, error)
	containerStartFn      func(ctx context.Context, containerID string, options container.StartOptions) error
	containerRenameFn     func(ctx context.Context, containerID string, newContainerName string) error
	networkConnectFn      func(ctx context.Context, networkID string, containerID string, config *network.EndpointSettings) error
	imagePullFn           func(ctx context.Context, ref string, options image.PullOptions) (io.ReadCloser, error)
	imageListFn           func(ctx context.Context, options image.ListOptions) ([]image.Summary, error)
	imageInspectWithRawFn func(ctx context.Context, imageRef string) (image.InspectResponse, []byte, error)
}

func (m *mockClient) ContainerList(ctx context.Context, options container.ListOptions) ([]container.Summary, error) {
	if m.containerListFn == nil {
		panic("mockClient.ContainerList called but not configured")
	}
	return m.containerListFn(ctx, options)
}

func (m *mockClient) ContainerInspect(ctx context.Context, containerID string) (container.InspectResponse, error) {
	if m.containerInspectFn == nil {
		panic("mockClient.ContainerInspect called but not configured")
	}
	return m.containerInspectFn(ctx, containerID)
}

func (m *mockClient) ContainerStop(ctx context.Context, containerID string, options container.StopOptions) error {
	if m.containerStopFn == nil {
		panic("mockClient.ContainerStop called but not configured")
	}
	return m.containerStopFn(ctx, containerID, options)
}

func (m *mockClient) ContainerRemove(ctx context.Context, containerID string, options container.RemoveOptions) error {
	if m.containerRemoveFn == nil {
		panic("mockClient.ContainerRemove called but not configured")
	}
	return m.containerRemoveFn(ctx, containerID, options)
}

func (m *mockClient) ContainerCreate(ctx context.Context, config *container.Config, hostConfig *container.HostConfig, networkingConfig *network.NetworkingConfig, platform *ocispec.Platform, containerName string) (container.CreateResponse, error) {
	if m.containerCreateFn == nil {
		panic("mockClient.ContainerCreate called but not configured")
	}
	return m.containerCreateFn(ctx, config, hostConfig, networkingConfig, platform, containerName)
}

func (m *mockClient) ContainerStart(ctx context.Context, containerID string, options container.StartOptions) error {
	if m.containerStartFn == nil {
		panic("mockClient.ContainerStart called but not configured")
	}
	return m.containerStartFn(ctx, containerID, options)
}

func (m *mockClient) ContainerRename(ctx context.Context, containerID string, newContainerName string) error {
	if m.containerRenameFn == nil {
		panic("mockClient.ContainerRename called but not configured")
	}
	return m.containerRenameFn(ctx, containerID, newContainerName)
}

func (m *mockClient) NetworkConnect(ctx context.Context, networkID string, containerID string, config *network.EndpointSettings) error {
	if m.networkConnectFn == nil {
		panic("mockClient.NetworkConnect called but not configured")
	}
	return m.networkConnectFn(ctx, networkID, containerID, config)
}

func (m *mockClient) ImagePull(ctx context.Context, ref string, options image.PullOptions) (io.ReadCloser, error) {
	if m.imagePullFn == nil {
		panic("mockClient.ImagePull called but not configured")
	}
	return m.imagePullFn(ctx, ref, options)
}

func (m *mockClient) ImageList(ctx context.Context, options image.ListOptions) ([]image.Summary, error) {
	if m.imageListFn == nil {
		panic("mockClient.ImageList called but not configured")
	}
	return m.imageListFn(ctx, options)
}

func (m *mockClient) ImageInspectWithRaw(ctx context.Context, imageRef string) (image.InspectResponse, []byte, error) {
	if m.imageInspectWithRawFn == nil {
		panic("mockClient.ImageInspectWithRaw called but not configured")
	}
	return m.imageInspectWithRawFn(ctx, imageRef)
}

// Compile-time check that mockClient implements DockerClient.
var _ DockerClient = (*mockClient)(nil)

// discardLogger returns a slog.Logger that writes nowhere, for quiet tests.
func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
