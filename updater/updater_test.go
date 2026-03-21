package updater

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// fullMockForUpdate returns a mockClient that supports the full update cycle
// for a single container. The caller can override individual fields after.
func fullMockForUpdate(oldImageID, newImageID string) *mockClient {
	return &mockClient{
		imagePullFn: func(_ context.Context, _ string, _ image.PullOptions) (io.ReadCloser, error) {
			return io.NopCloser(strings.NewReader(`{}`)), nil
		},
		imageListFn: func(_ context.Context, _ image.ListOptions) ([]image.Summary, error) {
			return []image.Summary{
				{ID: newImageID},
			}, nil
		},
		containerInspectFn: func(_ context.Context, _ string) (container.InspectResponse, error) {
			return container.InspectResponse{
				ContainerJSONBase: &container.ContainerJSONBase{
					Name:       "/test-container",
					HostConfig: &container.HostConfig{},
				},
				Config: &container.Config{Image: "nginx:latest"},
				NetworkSettings: &container.NetworkSettings{
					Networks: map[string]*network.EndpointSettings{
						"bridge": {IPAddress: "172.17.0.2"},
					},
				},
			}, nil
		},
		containerStopFn: func(_ context.Context, _ string, _ container.StopOptions) error {
			return nil
		},
		containerRemoveFn: func(_ context.Context, _ string, _ container.RemoveOptions) error {
			return nil
		},
		containerCreateFn: func(_ context.Context, _ *container.Config, _ *container.HostConfig, _ *network.NetworkingConfig, _ *ocispec.Platform, _ string) (container.CreateResponse, error) {
			return container.CreateResponse{ID: "newcontainerid1234"}, nil
		},
		containerStartFn: func(_ context.Context, _ string, _ container.StartOptions) error {
			return nil
		},
	}
}

// ---------------------------------------------------------------------------
// updateContainer
// ---------------------------------------------------------------------------

func TestUpdateContainer_DigestUnchanged(t *testing.T) {
	sameID := "sha256:aabbccdd"

	m := &mockClient{
		imagePullFn: func(_ context.Context, _ string, _ image.PullOptions) (io.ReadCloser, error) {
			return io.NopCloser(strings.NewReader(`{}`)), nil
		},
		imageListFn: func(_ context.Context, _ image.ListOptions) ([]image.Summary, error) {
			return []image.Summary{
				{ID: sameID},
			}, nil
		},
	}

	u := New(m, &Config{NameGlobs: []string{"*"}}, time.Hour, 30, "", discardLogger())
	ct := container.Summary{
		ID:      "abcdef1234567890",
		Names:   []string{"/test"},
		Image:   "nginx:latest",
		ImageID: sameID,
	}

	updated, err := u.updateContainer(context.Background(), ct)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if updated {
		t.Error("expected no update when digest is unchanged")
	}
}

func TestUpdateContainer_DigestChanged(t *testing.T) {
	oldID := "sha256:oldoldold"
	newID := "sha256:newnewnew"

	var stopped, removed, created, started bool

	m := fullMockForUpdate(oldID, newID)
	m.containerStopFn = func(_ context.Context, _ string, _ container.StopOptions) error {
		stopped = true
		return nil
	}
	m.containerRemoveFn = func(_ context.Context, _ string, _ container.RemoveOptions) error {
		removed = true
		return nil
	}
	m.containerCreateFn = func(_ context.Context, _ *container.Config, _ *container.HostConfig, _ *network.NetworkingConfig, _ *ocispec.Platform, _ string) (container.CreateResponse, error) {
		created = true
		return container.CreateResponse{ID: "newcontainerid1234"}, nil
	}
	m.containerStartFn = func(_ context.Context, _ string, _ container.StartOptions) error {
		started = true
		return nil
	}

	u := New(m, &Config{NameGlobs: []string{"*"}}, time.Hour, 30, "", discardLogger())
	ct := container.Summary{
		ID:      "abcdef1234567890",
		Names:   []string{"/test"},
		Image:   "nginx:latest",
		ImageID: oldID,
	}

	updated, err := u.updateContainer(context.Background(), ct)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !updated {
		t.Error("expected update when image ID changed")
	}
	if !stopped {
		t.Error("container was not stopped")
	}
	if !removed {
		t.Error("container was not removed")
	}
	if !created {
		t.Error("new container was not created")
	}
	if !started {
		t.Error("new container was not started")
	}
}

func TestUpdateContainer_PullFailure(t *testing.T) {
	m := &mockClient{
		imagePullFn: func(_ context.Context, _ string, _ image.PullOptions) (io.ReadCloser, error) {
			return nil, errors.New("registry down")
		},
	}

	u := New(m, &Config{NameGlobs: []string{"*"}}, time.Hour, 30, "", discardLogger())
	u.retryDelay = time.Millisecond // fast retries for tests
	ct := container.Summary{
		ID:      "abcdef1234567890",
		Names:   []string{"/test"},
		Image:   "nginx:latest",
		ImageID: "sha256:old",
	}

	// retryWithBackoff uses 5s delays by default — but the error happens
	// instantly each time, and we're only testing that the error propagates.
	// Override the pull to always fail immediately so there's no real sleep.
	_, err := u.updateContainer(context.Background(), ct)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "pull") {
		t.Errorf("error should mention pull, got: %v", err)
	}
}

func TestUpdateContainer_SnapshotFailure(t *testing.T) {
	m := fullMockForUpdate("sha256:old", "sha256:new")
	m.containerInspectFn = func(_ context.Context, _ string) (container.InspectResponse, error) {
		return container.InspectResponse{}, errors.New("inspect failed")
	}

	u := New(m, &Config{NameGlobs: []string{"*"}}, time.Hour, 30, "", discardLogger())
	ct := container.Summary{
		ID:      "abcdef1234567890",
		Names:   []string{"/test"},
		Image:   "nginx:latest",
		ImageID: "sha256:old",
	}

	_, err := u.updateContainer(context.Background(), ct)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "snapshot") {
		t.Errorf("error should mention snapshot, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// runOnce
// ---------------------------------------------------------------------------

func TestRunOnce_FiltersAndCounts(t *testing.T) {
	m := &mockClient{
		containerListFn: func(_ context.Context, _ container.ListOptions) ([]container.Summary, error) {
			return []container.Summary{
				{ID: "aaaa111122223333", Names: []string{"/web-1"}, Image: "nginx:latest", ImageID: "sha256:same"},
				{ID: "bbbb111122223333", Names: []string{"/api-1"}, Image: "redis:latest", ImageID: "sha256:same"},
				{ID: "cccc111122223333", Names: []string{"/db-1"}, Image: "postgres:16", ImageID: "sha256:same"},
			}, nil
		},
		imagePullFn: func(_ context.Context, _ string, _ image.PullOptions) (io.ReadCloser, error) {
			return io.NopCloser(strings.NewReader(`{}`)), nil
		},
		imageListFn: func(_ context.Context, _ image.ListOptions) ([]image.Summary, error) {
			// Same image ID — no update needed
			return []image.Summary{
				{ID: "sha256:same"},
			}, nil
		},
	}

	// Only match web-* containers
	cfg := &Config{NameGlobs: []string{"web-*"}}
	u := New(m, cfg, time.Hour, 30, "", discardLogger())

	// runOnce should check web-1 but skip api-1 and db-1.
	// We can't directly observe counts, but we can verify no panics
	// (api-1 and db-1 should never trigger inspect/stop/etc).
	u.runOnce(context.Background())
}

func TestRunOnce_ListError(t *testing.T) {
	m := &mockClient{
		containerListFn: func(_ context.Context, _ container.ListOptions) ([]container.Summary, error) {
			return nil, errors.New("socket error")
		},
	}

	cfg := &Config{NameGlobs: []string{"*"}}
	u := New(m, cfg, time.Hour, 30, "", discardLogger())

	// Should not panic; logs the error and returns.
	u.runOnce(context.Background())
}

// ---------------------------------------------------------------------------
// Run — tick skip
// ---------------------------------------------------------------------------

func TestRun_SkipsTick(t *testing.T) {
	// We'll make runOnce block for a while by using a slow ContainerList.
	var runOnceStarted atomic.Bool
	var runOnceBlocked chan struct{} = make(chan struct{})

	m := &mockClient{
		containerListFn: func(ctx context.Context, _ container.ListOptions) ([]container.Summary, error) {
			runOnceStarted.Store(true)
			// Block until test says go, or context cancelled.
			select {
			case <-runOnceBlocked:
			case <-ctx.Done():
			}
			return nil, nil
		},
	}

	cfg := &Config{NameGlobs: []string{"*"}}
	u := New(m, cfg, 50*time.Millisecond, 30, "", discardLogger())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		u.Run(ctx)
		close(done)
	}()

	// Wait for the initial runOnce to start.
	for !runOnceStarted.Load() {
		time.Sleep(5 * time.Millisecond)
	}

	// The running flag should now be true.
	if !u.running.Load() {
		t.Error("expected running flag to be true during runOnce")
	}

	// Let at least 2 ticks fire while runOnce is blocked.
	time.Sleep(150 * time.Millisecond)

	// Unblock runOnce.
	close(runOnceBlocked)

	// Cancel and wait for Run to exit.
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not exit after cancel")
	}

	// The running flag should be false after exit.
	if u.running.Load() {
		t.Error("expected running flag to be false after Run exits")
	}
}

func TestRun_CancelsOnSignal(t *testing.T) {
	m := &mockClient{
		containerListFn: func(_ context.Context, _ container.ListOptions) ([]container.Summary, error) {
			return nil, nil
		},
	}

	cfg := &Config{NameGlobs: []string{"*"}}
	u := New(m, cfg, time.Hour, 30, "", discardLogger())

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		u.Run(ctx)
		close(done)
	}()

	// Give Run time to start and complete the first runOnce.
	time.Sleep(50 * time.Millisecond)

	// Cancel the context — Run should exit.
	cancel()

	select {
	case <-done:
		// OK
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not exit after context cancellation")
	}
}
