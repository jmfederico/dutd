package updater

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// ---------------------------------------------------------------------------
// shortDigest
// ---------------------------------------------------------------------------

func TestShortDigest(t *testing.T) {
	tests := []struct {
		input  string
		expect string
	}{
		{"sha256:abcdef1234567890abcdef", "abcdef123456"},
		{"abcdef1234567890abcdef", "abcdef123456"},
		{"sha256:short", "short"},
		{"tiny", "tiny"},
		{"", ""},
		{"sha256:", ""},
		{"exactly12ch", "exactly12ch"},
		{"exactly12chX", "exactly12chX"},
		{"exactly12chXY", "exactly12chX"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := shortDigest(tt.input)
			if got != tt.expect {
				t.Errorf("shortDigest(%q) = %q, want %q", tt.input, got, tt.expect)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// retryWithBackoff
// ---------------------------------------------------------------------------

func TestRetryWithBackoff_SucceedsFirstTry(t *testing.T) {
	calls := 0
	err := retryWithBackoff(3, time.Millisecond, func() error {
		calls++
		return nil
	})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if calls != 1 {
		t.Fatalf("expected 1 call, got %d", calls)
	}
}

func TestRetryWithBackoff_SucceedsOnRetry(t *testing.T) {
	calls := 0
	err := retryWithBackoff(3, time.Millisecond, func() error {
		calls++
		if calls < 3 {
			return errors.New("transient")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if calls != 3 {
		t.Fatalf("expected 3 calls, got %d", calls)
	}
}

func TestRetryWithBackoff_ExhaustsAttempts(t *testing.T) {
	calls := 0
	err := retryWithBackoff(3, time.Millisecond, func() error {
		calls++
		return errors.New("permanent")
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if calls != 3 {
		t.Fatalf("expected 3 calls, got %d", calls)
	}
}

func TestRetryWithBackoff_SingleAttempt(t *testing.T) {
	err := retryWithBackoff(1, time.Millisecond, func() error {
		return errors.New("fail")
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

// ---------------------------------------------------------------------------
// pullImage
// ---------------------------------------------------------------------------

func TestPullImage_Success(t *testing.T) {
	m := &mockClient{
		imagePullFn: func(_ context.Context, ref string, _ image.PullOptions) (io.ReadCloser, error) {
			if ref != "nginx:latest" {
				t.Errorf("unexpected ref %q", ref)
			}
			return io.NopCloser(strings.NewReader(`{"status":"done"}`)), nil
		},
	}

	err := pullImage(context.Background(), m, "nginx:latest", discardLogger())
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
}

func TestPullImage_PullError(t *testing.T) {
	m := &mockClient{
		imagePullFn: func(_ context.Context, _ string, _ image.PullOptions) (io.ReadCloser, error) {
			return nil, errors.New("registry unreachable")
		},
	}

	err := pullImage(context.Background(), m, "nginx:latest", discardLogger())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "registry unreachable") {
		t.Errorf("error should contain cause, got: %v", err)
	}
}

func TestPullImage_DrainError(t *testing.T) {
	m := &mockClient{
		imagePullFn: func(_ context.Context, _ string, _ image.PullOptions) (io.ReadCloser, error) {
			return io.NopCloser(&failReader{}), nil
		},
	}

	err := pullImage(context.Background(), m, "nginx:latest", discardLogger())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "drain") {
		t.Errorf("error should mention drain, got: %v", err)
	}
}

// failReader is an io.Reader that always returns an error.
type failReader struct{}

func (f *failReader) Read([]byte) (int, error) {
	return 0, errors.New("read failed")
}

// ---------------------------------------------------------------------------
// imageID
// ---------------------------------------------------------------------------

func TestImageID_Found(t *testing.T) {
	m := &mockClient{
		imageInspectWithRawFn: func(_ context.Context, ref string) (image.InspectResponse, []byte, error) {
			if ref != "nginx:latest" {
				t.Errorf("unexpected ref %q", ref)
			}
			return image.InspectResponse{ID: "sha256:abc123def456"}, nil, nil
		},
	}

	id, err := imageID(context.Background(), m, "nginx:latest")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != "sha256:abc123def456" {
		t.Errorf("got id %q, want %q", id, "sha256:abc123def456")
	}
}

func TestImageID_Error(t *testing.T) {
	m := &mockClient{
		imageInspectWithRawFn: func(_ context.Context, _ string) (image.InspectResponse, []byte, error) {
			return image.InspectResponse{}, nil, errors.New("api error")
		},
	}

	_, err := imageID(context.Background(), m, "nginx:latest")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

// ---------------------------------------------------------------------------
// stopAndRemove
// ---------------------------------------------------------------------------

func TestStopAndRemove_Success(t *testing.T) {
	var stopped, removed bool
	id := "abcdef1234567890"

	m := &mockClient{
		containerStopFn: func(_ context.Context, cid string, opts container.StopOptions) error {
			if cid != id {
				t.Errorf("stop: unexpected ID %q", cid)
			}
			if opts.Timeout == nil || *opts.Timeout != 30 {
				t.Errorf("stop: expected timeout 30, got %v", opts.Timeout)
			}
			stopped = true
			return nil
		},
		containerRemoveFn: func(_ context.Context, cid string, opts container.RemoveOptions) error {
			if cid != id {
				t.Errorf("remove: unexpected ID %q", cid)
			}
			if opts.Force {
				t.Error("remove: Force should be false")
			}
			removed = true
			return nil
		},
	}

	err := stopAndRemove(context.Background(), m, id, 30, discardLogger())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !stopped {
		t.Error("container was not stopped")
	}
	if !removed {
		t.Error("container was not removed")
	}
}

func TestStopAndRemove_StopError(t *testing.T) {
	m := &mockClient{
		containerStopFn: func(_ context.Context, _ string, _ container.StopOptions) error {
			return errors.New("stop failed")
		},
	}

	err := stopAndRemove(context.Background(), m, "abcdef1234567890", 30, discardLogger())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "stop") {
		t.Errorf("error should mention stop, got: %v", err)
	}
}

func TestStopAndRemove_RemoveError(t *testing.T) {
	m := &mockClient{
		containerStopFn: func(_ context.Context, _ string, _ container.StopOptions) error {
			return nil
		},
		containerRemoveFn: func(_ context.Context, _ string, _ container.RemoveOptions) error {
			return errors.New("remove failed")
		},
	}

	err := stopAndRemove(context.Background(), m, "abcdef1234567890", 30, discardLogger())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "remove") {
		t.Errorf("error should mention remove, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// snapshotContainer
// ---------------------------------------------------------------------------

func TestSnapshotContainer_Success(t *testing.T) {
	m := &mockClient{
		containerInspectFn: func(_ context.Context, cid string) (container.InspectResponse, error) {
			return container.InspectResponse{
				ContainerJSONBase: &container.ContainerJSONBase{
					Name: "/my-container",
					HostConfig: &container.HostConfig{
						RestartPolicy: container.RestartPolicy{Name: container.RestartPolicyUnlessStopped},
					},
				},
				Config: &container.Config{
					Image: "nginx:latest",
					Env:   []string{"FOO=bar"},
				},
				NetworkSettings: &container.NetworkSettings{
					Networks: map[string]*network.EndpointSettings{
						"bridge": {
							Aliases:   []string{"web"},
							IPAddress: "172.17.0.2",
						},
					},
				},
			}, nil
		},
	}

	info, err := snapshotContainer(context.Background(), m, "abcdef1234567890")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if info.Name != "my-container" {
		t.Errorf("expected name %q, got %q", "my-container", info.Name)
	}
	if info.Config.Image != "nginx:latest" {
		t.Errorf("expected image %q, got %q", "nginx:latest", info.Config.Image)
	}
	if len(info.Config.Env) != 1 || info.Config.Env[0] != "FOO=bar" {
		t.Errorf("unexpected env: %v", info.Config.Env)
	}
	if info.HostConfig.RestartPolicy.Name != container.RestartPolicyUnlessStopped {
		t.Errorf("unexpected restart policy: %v", info.HostConfig.RestartPolicy)
	}

	net, ok := info.NetworkSettings.Networks["bridge"]
	if !ok {
		t.Fatal("bridge network not found in snapshot")
	}
	if net.IPAddress != "172.17.0.2" {
		t.Errorf("expected IP 172.17.0.2, got %s", net.IPAddress)
	}
	if len(net.Aliases) != 1 || net.Aliases[0] != "web" {
		t.Errorf("unexpected aliases: %v", net.Aliases)
	}
}

func TestSnapshotContainer_NilNetworkSettings(t *testing.T) {
	m := &mockClient{
		containerInspectFn: func(_ context.Context, _ string) (container.InspectResponse, error) {
			return container.InspectResponse{
				ContainerJSONBase: &container.ContainerJSONBase{
					Name:       "/orphan",
					HostConfig: &container.HostConfig{},
				},
				Config:          &container.Config{Image: "alpine:latest"},
				NetworkSettings: nil,
			}, nil
		},
	}

	info, err := snapshotContainer(context.Background(), m, "abcdef1234567890")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(info.NetworkSettings.Networks) != 0 {
		t.Errorf("expected empty networks, got %v", info.NetworkSettings.Networks)
	}
}

func TestSnapshotContainer_Error(t *testing.T) {
	m := &mockClient{
		containerInspectFn: func(_ context.Context, _ string) (container.InspectResponse, error) {
			return container.InspectResponse{}, errors.New("inspect failed")
		},
	}

	_, err := snapshotContainer(context.Background(), m, "abcdef1234567890")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

// ---------------------------------------------------------------------------
// recreate
// ---------------------------------------------------------------------------

func TestRecreate_SingleNetwork(t *testing.T) {
	newID := "newcontainer123456"
	var createdName, createdImage, startedID string

	m := &mockClient{
		containerCreateFn: func(_ context.Context, cfg *container.Config, _ *container.HostConfig, netCfg *network.NetworkingConfig, _ *ocispec.Platform, name string) (container.CreateResponse, error) {
			createdName = name
			createdImage = cfg.Image
			if _, ok := netCfg.EndpointsConfig["bridge"]; !ok {
				t.Error("expected bridge in networking config")
			}
			return container.CreateResponse{ID: newID}, nil
		},
		containerStartFn: func(_ context.Context, cid string, _ container.StartOptions) error {
			startedID = cid
			return nil
		},
	}

	info := containerInfo{
		Name:       "web-app",
		Config:     &container.Config{Image: "nginx:old"},
		HostConfig: &container.HostConfig{},
		NetworkSettings: networkSnapshot{
			Networks: map[string]*networkEndpoint{
				"bridge": {Aliases: []string{"web"}, IPAddress: "172.17.0.2"},
			},
		},
	}

	err := recreate(context.Background(), m, info, "nginx:new", discardLogger())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if createdName != "web-app" {
		t.Errorf("expected name %q, got %q", "web-app", createdName)
	}
	if createdImage != "nginx:new" {
		t.Errorf("expected image %q, got %q", "nginx:new", createdImage)
	}
	if startedID != newID {
		t.Errorf("expected started ID %q, got %q", newID, startedID)
	}
}

func TestRecreate_MultipleNetworks(t *testing.T) {
	newID := "newcontainer123456"
	var connectedNetworks []string

	m := &mockClient{
		containerCreateFn: func(_ context.Context, _ *container.Config, _ *container.HostConfig, _ *network.NetworkingConfig, _ *ocispec.Platform, _ string) (container.CreateResponse, error) {
			return container.CreateResponse{ID: newID}, nil
		},
		containerStartFn: func(_ context.Context, _ string, _ container.StartOptions) error {
			return nil
		},
		networkConnectFn: func(_ context.Context, networkID string, containerID string, _ *network.EndpointSettings) error {
			connectedNetworks = append(connectedNetworks, networkID)
			return nil
		},
	}

	info := containerInfo{
		Name:       "multi-net",
		Config:     &container.Config{Image: "app:v1"},
		HostConfig: &container.HostConfig{},
		NetworkSettings: networkSnapshot{
			Networks: map[string]*networkEndpoint{
				"bridge":  {Aliases: []string{"a"}, IPAddress: "172.17.0.2"},
				"custom1": {Aliases: []string{"b"}, IPAddress: "10.0.0.2"},
				"custom2": {Aliases: []string{"c"}, IPAddress: "10.0.1.2"},
			},
		},
	}

	err := recreate(context.Background(), m, info, "app:v2", discardLogger())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// One network is attached at creation time; the other two via NetworkConnect.
	if len(connectedNetworks) != 2 {
		t.Errorf("expected 2 extra network connections, got %d: %v", len(connectedNetworks), connectedNetworks)
	}
}

func TestRecreate_NetworkConnectFailure_NonFatal(t *testing.T) {
	newID := "newcontainer123456"

	m := &mockClient{
		containerCreateFn: func(_ context.Context, _ *container.Config, _ *container.HostConfig, _ *network.NetworkingConfig, _ *ocispec.Platform, _ string) (container.CreateResponse, error) {
			return container.CreateResponse{ID: newID}, nil
		},
		containerStartFn: func(_ context.Context, _ string, _ container.StartOptions) error {
			return nil
		},
		networkConnectFn: func(_ context.Context, _ string, _ string, _ *network.EndpointSettings) error {
			return errors.New("network gone")
		},
	}

	info := containerInfo{
		Name:       "app",
		Config:     &container.Config{Image: "app:v1"},
		HostConfig: &container.HostConfig{},
		NetworkSettings: networkSnapshot{
			Networks: map[string]*networkEndpoint{
				"bridge": {IPAddress: "172.17.0.2"},
				"custom": {IPAddress: "10.0.0.2"},
			},
		},
	}

	// Should succeed despite NetworkConnect failure (non-fatal).
	err := recreate(context.Background(), m, info, "app:v2", discardLogger())
	if err != nil {
		t.Fatalf("expected no error (network connect is non-fatal), got: %v", err)
	}
}

func TestRecreate_CreateError(t *testing.T) {
	m := &mockClient{
		containerCreateFn: func(_ context.Context, _ *container.Config, _ *container.HostConfig, _ *network.NetworkingConfig, _ *ocispec.Platform, _ string) (container.CreateResponse, error) {
			return container.CreateResponse{}, errors.New("create failed")
		},
	}

	info := containerInfo{
		Name:       "app",
		Config:     &container.Config{Image: "app:v1"},
		HostConfig: &container.HostConfig{},
		NetworkSettings: networkSnapshot{
			Networks: map[string]*networkEndpoint{
				"bridge": {IPAddress: "172.17.0.2"},
			},
		},
	}

	err := recreate(context.Background(), m, info, "app:v2", discardLogger())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "create") {
		t.Errorf("error should mention create, got: %v", err)
	}
}

func TestRecreate_StartError(t *testing.T) {
	m := &mockClient{
		containerCreateFn: func(_ context.Context, _ *container.Config, _ *container.HostConfig, _ *network.NetworkingConfig, _ *ocispec.Platform, _ string) (container.CreateResponse, error) {
			return container.CreateResponse{ID: "newcontainer123456"}, nil
		},
		containerStartFn: func(_ context.Context, _ string, _ container.StartOptions) error {
			return errors.New("start failed")
		},
	}

	info := containerInfo{
		Name:       "app",
		Config:     &container.Config{Image: "app:v1"},
		HostConfig: &container.HostConfig{},
		NetworkSettings: networkSnapshot{
			Networks: map[string]*networkEndpoint{
				"bridge": {IPAddress: "172.17.0.2"},
			},
		},
	}

	err := recreate(context.Background(), m, info, "app:v2", discardLogger())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "start") {
		t.Errorf("error should mention start, got: %v", err)
	}
}
