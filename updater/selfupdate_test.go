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
// isHex
// ---------------------------------------------------------------------------

func TestIsHex(t *testing.T) {
	tests := []struct {
		input  string
		expect bool
	}{
		{"abcdef123456", true},
		{"ABCDEF123456", true},
		{"0123456789abcdef", true},
		{"", false},
		{"not-hex!", false},
		{"abcdef12345g", false},
		{"abc def", false},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			if got := isHex(tt.input); got != tt.expect {
				t.Errorf("isHex(%q) = %v, want %v", tt.input, got, tt.expect)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// isSelf
// ---------------------------------------------------------------------------

func TestIsSelf(t *testing.T) {
	tests := []struct {
		name   string
		selfID string
		ctID   string
		expect bool
	}{
		{
			name:   "exact match",
			selfID: "abcdef123456",
			ctID:   "abcdef123456",
			expect: true,
		},
		{
			name:   "selfID is prefix of ctID (HOSTNAME is short form)",
			selfID: "abcdef123456",
			ctID:   "abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890",
			expect: true,
		},
		{
			name:   "ctID is prefix of selfID",
			selfID: "abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890",
			ctID:   "abcdef123456",
			expect: true,
		},
		{
			name:   "no match",
			selfID: "abcdef123456",
			ctID:   "999999999999",
			expect: false,
		},
		{
			name:   "empty selfID",
			selfID: "",
			ctID:   "abcdef123456",
			expect: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			u := &Updater{selfID: tt.selfID}
			ct := container.Summary{ID: tt.ctID}
			if got := u.isSelf(ct); got != tt.expect {
				t.Errorf("isSelf() = %v, want %v", got, tt.expect)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// CleanupPredecessor
// ---------------------------------------------------------------------------

func TestCleanupPredecessor_NotASuccessor(t *testing.T) {
	// When there are no predecessor labels, cleanup should be a no-op.
	m := &mockClient{
		containerInspectFn: func(_ context.Context, _ string) (container.InspectResponse, error) {
			return container.InspectResponse{
				ContainerJSONBase: &container.ContainerJSONBase{
					Name:       "/dutd",
					HostConfig: &container.HostConfig{},
				},
				Config: &container.Config{
					Image:  "dutd:latest",
					Labels: map[string]string{},
				},
			}, nil
		},
	}

	err := CleanupPredecessor(context.Background(), m, "abcdef123456", discardLogger())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCleanupPredecessor_EmptySelfID(t *testing.T) {
	// When selfID is empty, cleanup should be a no-op.
	err := CleanupPredecessor(context.Background(), nil, "", discardLogger())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCleanupPredecessor_SuccessfulCleanup(t *testing.T) {
	var stopped, removed, renamed bool
	predecessorID := "oldcontainer1234567890"
	predecessorName := "dutd"

	m := &mockClient{
		containerInspectFn: func(_ context.Context, id string) (container.InspectResponse, error) {
			return container.InspectResponse{
				ContainerJSONBase: &container.ContainerJSONBase{
					Name:       "/dutd-dutd-next",
					HostConfig: &container.HostConfig{},
				},
				Config: &container.Config{
					Image: "dutd:latest",
					Labels: map[string]string{
						labelPredecessor:     predecessorID,
						labelPredecessorName: predecessorName,
					},
				},
			}, nil
		},
		containerStopFn: func(_ context.Context, id string, _ container.StopOptions) error {
			if id != predecessorID {
				t.Errorf("stopped wrong container: %q", id)
			}
			stopped = true
			return nil
		},
		containerRemoveFn: func(_ context.Context, id string, _ container.RemoveOptions) error {
			if id != predecessorID {
				t.Errorf("removed wrong container: %q", id)
			}
			removed = true
			return nil
		},
		containerRenameFn: func(_ context.Context, id string, newName string) error {
			if newName != predecessorName {
				t.Errorf("renamed to %q, want %q", newName, predecessorName)
			}
			renamed = true
			return nil
		},
	}

	err := CleanupPredecessor(context.Background(), m, "successor123456", discardLogger())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !stopped {
		t.Error("predecessor was not stopped")
	}
	if !removed {
		t.Error("predecessor was not removed")
	}
	if !renamed {
		t.Error("successor was not renamed")
	}
}

func TestCleanupPredecessor_RemoveFailureNonFatal(t *testing.T) {
	m := &mockClient{
		containerInspectFn: func(_ context.Context, _ string) (container.InspectResponse, error) {
			return container.InspectResponse{
				ContainerJSONBase: &container.ContainerJSONBase{
					Name:       "/dutd-dutd-next",
					HostConfig: &container.HostConfig{},
				},
				Config: &container.Config{
					Image: "dutd:latest",
					Labels: map[string]string{
						labelPredecessor:     "oldcontainer1234",
						labelPredecessorName: "dutd",
					},
				},
			}, nil
		},
		containerStopFn: func(_ context.Context, _ string, _ container.StopOptions) error {
			return nil
		},
		containerRemoveFn: func(_ context.Context, _ string, _ container.RemoveOptions) error {
			return errors.New("already removed")
		},
		containerRenameFn: func(_ context.Context, _ string, _ string) error {
			return nil
		},
	}

	// Should succeed — remove failure is logged as a warning, not an error.
	err := CleanupPredecessor(context.Background(), m, "successor123456", discardLogger())
	if err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}
}

func TestCleanupPredecessor_RenameFailure(t *testing.T) {
	m := &mockClient{
		containerInspectFn: func(_ context.Context, _ string) (container.InspectResponse, error) {
			return container.InspectResponse{
				ContainerJSONBase: &container.ContainerJSONBase{
					Name:       "/dutd-dutd-next",
					HostConfig: &container.HostConfig{},
				},
				Config: &container.Config{
					Image: "dutd:latest",
					Labels: map[string]string{
						labelPredecessor:     "oldcontainer1234",
						labelPredecessorName: "dutd",
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
		containerRenameFn: func(_ context.Context, _ string, _ string) error {
			return errors.New("rename failed")
		},
	}

	err := CleanupPredecessor(context.Background(), m, "successor123456", discardLogger())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "rename") {
		t.Errorf("error should mention rename, got: %v", err)
	}
}

func TestCleanupPredecessor_SkipRenameWhenNameAlreadyCorrect(t *testing.T) {
	// If the container name already matches the predecessor name
	// (e.g. name conflict was resolved another way), skip rename.
	m := &mockClient{
		containerInspectFn: func(_ context.Context, _ string) (container.InspectResponse, error) {
			return container.InspectResponse{
				ContainerJSONBase: &container.ContainerJSONBase{
					Name:       "/dutd", // already the correct name
					HostConfig: &container.HostConfig{},
				},
				Config: &container.Config{
					Image: "dutd:latest",
					Labels: map[string]string{
						labelPredecessor:     "oldcontainer1234",
						labelPredecessorName: "dutd",
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
		// containerRenameFn not set — would panic if called
	}

	err := CleanupPredecessor(context.Background(), m, "successor123456", discardLogger())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// ---------------------------------------------------------------------------
// selfUpdate
// ---------------------------------------------------------------------------

func TestSelfUpdate_Success(t *testing.T) {
	var createdName, createdImage string
	var createdLabels map[string]string
	var started bool

	m := &mockClient{
		containerInspectFn: func(_ context.Context, _ string) (container.InspectResponse, error) {
			return container.InspectResponse{
				ContainerJSONBase: &container.ContainerJSONBase{
					Name:       "/dutd",
					HostConfig: &container.HostConfig{},
				},
				Config: &container.Config{
					Image:  "dutd:latest",
					Labels: map[string]string{},
				},
				NetworkSettings: &container.NetworkSettings{
					Networks: map[string]*network.EndpointSettings{
						"bridge": {IPAddress: "172.17.0.2"},
					},
				},
			}, nil
		},
		containerCreateFn: func(_ context.Context, cfg *container.Config, _ *container.HostConfig, _ *network.NetworkingConfig, _ *ocispec.Platform, name string) (container.CreateResponse, error) {
			createdName = name
			createdImage = cfg.Image
			createdLabels = cfg.Labels
			return container.CreateResponse{ID: "successorid1234567890"}, nil
		},
		containerStartFn: func(_ context.Context, id string, _ container.StartOptions) error {
			started = true
			return nil
		},
	}

	selfID := "selfcontainer123"
	u := New(m, &Config{NameGlobs: []string{"*"}}, time.Hour, 30, selfID, discardLogger())

	ct := container.Summary{
		ID:      "selfcontainer1234567890",
		Names:   []string{"/dutd"},
		Image:   "dutd:latest",
		ImageID: "sha256:old",
	}

	err := u.selfUpdate(context.Background(), ct, "dutd:latest")
	if !errors.Is(err, ErrSelfUpdateRestart) {
		t.Fatalf("expected ErrSelfUpdateRestart, got: %v", err)
	}

	if createdName != "dutd"+selfUpdateSuffix {
		t.Errorf("successor name = %q, want %q", createdName, "dutd"+selfUpdateSuffix)
	}
	if createdImage != "dutd:latest" {
		t.Errorf("successor image = %q, want %q", createdImage, "dutd:latest")
	}
	if !started {
		t.Error("successor was not started")
	}

	// Verify labels.
	if createdLabels[labelPredecessor] != ct.ID {
		t.Errorf("predecessor label = %q, want %q", createdLabels[labelPredecessor], ct.ID)
	}
	if createdLabels[labelPredecessorName] != "dutd" {
		t.Errorf("predecessor name label = %q, want %q", createdLabels[labelPredecessorName], "dutd")
	}
}

func TestSelfUpdate_SnapshotError(t *testing.T) {
	m := &mockClient{
		containerInspectFn: func(_ context.Context, _ string) (container.InspectResponse, error) {
			return container.InspectResponse{}, errors.New("inspect failed")
		},
	}

	u := New(m, &Config{NameGlobs: []string{"*"}}, time.Hour, 30, "self123456789", discardLogger())

	ct := container.Summary{
		ID:    "self1234567890abcdef",
		Names: []string{"/dutd"},
		Image: "dutd:latest",
	}

	err := u.selfUpdate(context.Background(), ct, "dutd:latest")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "snapshot") {
		t.Errorf("error should mention snapshot, got: %v", err)
	}
}

func TestSelfUpdate_CreateError(t *testing.T) {
	m := &mockClient{
		containerInspectFn: func(_ context.Context, _ string) (container.InspectResponse, error) {
			return container.InspectResponse{
				ContainerJSONBase: &container.ContainerJSONBase{
					Name:       "/dutd",
					HostConfig: &container.HostConfig{},
				},
				Config: &container.Config{
					Image:  "dutd:latest",
					Labels: map[string]string{},
				},
				NetworkSettings: &container.NetworkSettings{
					Networks: map[string]*network.EndpointSettings{
						"bridge": {IPAddress: "172.17.0.2"},
					},
				},
			}, nil
		},
		containerCreateFn: func(_ context.Context, _ *container.Config, _ *container.HostConfig, _ *network.NetworkingConfig, _ *ocispec.Platform, _ string) (container.CreateResponse, error) {
			return container.CreateResponse{}, errors.New("create failed")
		},
	}

	u := New(m, &Config{NameGlobs: []string{"*"}}, time.Hour, 30, "self123456789", discardLogger())

	ct := container.Summary{
		ID:    "self1234567890abcdef",
		Names: []string{"/dutd"},
		Image: "dutd:latest",
	}

	err := u.selfUpdate(context.Background(), ct, "dutd:latest")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "create") {
		t.Errorf("error should mention create, got: %v", err)
	}
}

func TestSelfUpdate_StartError(t *testing.T) {
	m := &mockClient{
		containerInspectFn: func(_ context.Context, _ string) (container.InspectResponse, error) {
			return container.InspectResponse{
				ContainerJSONBase: &container.ContainerJSONBase{
					Name:       "/dutd",
					HostConfig: &container.HostConfig{},
				},
				Config: &container.Config{
					Image:  "dutd:latest",
					Labels: map[string]string{},
				},
				NetworkSettings: &container.NetworkSettings{
					Networks: map[string]*network.EndpointSettings{
						"bridge": {IPAddress: "172.17.0.2"},
					},
				},
			}, nil
		},
		containerCreateFn: func(_ context.Context, _ *container.Config, _ *container.HostConfig, _ *network.NetworkingConfig, _ *ocispec.Platform, _ string) (container.CreateResponse, error) {
			return container.CreateResponse{ID: "successorid1234567890"}, nil
		},
		containerStartFn: func(_ context.Context, _ string, _ container.StartOptions) error {
			return errors.New("start failed")
		},
	}

	u := New(m, &Config{NameGlobs: []string{"*"}}, time.Hour, 30, "self123456789", discardLogger())

	ct := container.Summary{
		ID:    "self1234567890abcdef",
		Names: []string{"/dutd"},
		Image: "dutd:latest",
	}

	err := u.selfUpdate(context.Background(), ct, "dutd:latest")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "start") {
		t.Errorf("error should mention start, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// updateSelf
// ---------------------------------------------------------------------------

func TestUpdateSelf_AlreadyUpToDate(t *testing.T) {
	sameID := "sha256:aabbccdd"

	m := &mockClient{
		containerInspectFn: func(_ context.Context, _ string) (container.InspectResponse, error) {
			return container.InspectResponse{
				Config: &container.Config{Image: "dutd:latest"},
			}, nil
		},
		imagePullFn: func(_ context.Context, _ string, _ image.PullOptions) (io.ReadCloser, error) {
			return io.NopCloser(strings.NewReader(`{}`)), nil
		},
		imageInspectWithRawFn: func(_ context.Context, _ string) (image.InspectResponse, []byte, error) {
			return image.InspectResponse{
				ID:          sameID,
				RepoDigests: []string{"dutd@sha256:abc123"},
			}, nil, nil
		},
	}

	u := New(m, &Config{NameGlobs: []string{"*"}}, time.Hour, 30, "self123456789", discardLogger())

	ct := container.Summary{
		ID:      "self1234567890abcdef",
		Names:   []string{"/dutd"},
		Image:   "dutd:latest",
		ImageID: sameID,
	}

	restarted, err := u.updateSelf(context.Background(), ct)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if restarted {
		t.Error("expected no restart when image is unchanged")
	}
}

func TestUpdateSelf_ImageChanged(t *testing.T) {
	m := &mockClient{
		imagePullFn: func(_ context.Context, _ string, _ image.PullOptions) (io.ReadCloser, error) {
			return io.NopCloser(strings.NewReader(`{}`)), nil
		},
		imageInspectWithRawFn: func(_ context.Context, _ string) (image.InspectResponse, []byte, error) {
			return image.InspectResponse{
				ID:          "sha256:newnewnew",
				RepoDigests: []string{"dutd@sha256:abc123"},
			}, nil, nil
		},
		containerInspectFn: func(_ context.Context, _ string) (container.InspectResponse, error) {
			return container.InspectResponse{
				ContainerJSONBase: &container.ContainerJSONBase{
					Name:       "/dutd",
					HostConfig: &container.HostConfig{},
				},
				Config: &container.Config{
					Image:  "dutd:latest",
					Labels: map[string]string{},
				},
				NetworkSettings: &container.NetworkSettings{
					Networks: map[string]*network.EndpointSettings{
						"bridge": {IPAddress: "172.17.0.2"},
					},
				},
			}, nil
		},
		containerCreateFn: func(_ context.Context, _ *container.Config, _ *container.HostConfig, _ *network.NetworkingConfig, _ *ocispec.Platform, _ string) (container.CreateResponse, error) {
			return container.CreateResponse{ID: "successorid1234567890"}, nil
		},
		containerStartFn: func(_ context.Context, _ string, _ container.StartOptions) error {
			return nil
		},
	}

	u := New(m, &Config{NameGlobs: []string{"*"}}, time.Hour, 30, "self123456789", discardLogger())

	ct := container.Summary{
		ID:      "self1234567890abcdef",
		Names:   []string{"/dutd"},
		Image:   "dutd:latest",
		ImageID: "sha256:oldoldold",
	}

	restarted, err := u.updateSelf(context.Background(), ct)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !restarted {
		t.Error("expected restart when image changed")
	}
}

// ---------------------------------------------------------------------------
// runOnce — self is deferred to end
// ---------------------------------------------------------------------------

func TestRunOnce_DefersSelftUpdateToEnd(t *testing.T) {
	selfContainerID := "selfcont1234567890abcdef"
	otherContainerID := "othercont1234567890abcdef"

	var updateOrder []string

	sameID := "sha256:same"

	imageMap := map[string]string{
		selfContainerID:  "dutd:latest",
		otherContainerID: "nginx:latest",
	}

	m := &mockClient{
		containerListFn: func(_ context.Context, _ container.ListOptions) ([]container.Summary, error) {
			return []container.Summary{
				// Self is listed first — but should be processed last.
				{ID: selfContainerID, Names: []string{"/dutd"}, Image: "dutd:latest", ImageID: sameID},
				{ID: otherContainerID, Names: []string{"/web"}, Image: "nginx:latest", ImageID: sameID},
			}, nil
		},
		containerInspectFn: func(_ context.Context, cid string) (container.InspectResponse, error) {
			return container.InspectResponse{
				Config: &container.Config{Image: imageMap[cid]},
			}, nil
		},
		imagePullFn: func(_ context.Context, ref string, _ image.PullOptions) (io.ReadCloser, error) {
			updateOrder = append(updateOrder, ref)
			return io.NopCloser(strings.NewReader(`{}`)), nil
		},
		imageInspectWithRawFn: func(_ context.Context, _ string) (image.InspectResponse, []byte, error) {
			// Same image — no actual update
			return image.InspectResponse{
				ID:          sameID,
				RepoDigests: []string{"nginx@sha256:abc123"},
			}, nil, nil
		},
	}

	cfg := &Config{NameGlobs: []string{"*"}}
	u := New(m, cfg, time.Hour, 30, "selfcont1234", discardLogger())

	u.runOnce(context.Background())

	// Both should have been checked. The other container should be processed
	// before the self container.
	if len(updateOrder) != 2 {
		t.Fatalf("expected 2 pulls, got %d: %v", len(updateOrder), updateOrder)
	}
	if updateOrder[0] != "nginx:latest" {
		t.Errorf("expected other container first, got %q", updateOrder[0])
	}
	if updateOrder[1] != "dutd:latest" {
		t.Errorf("expected self container second, got %q", updateOrder[1])
	}
}

func TestRunOnce_SelfUpdateTriggersReturn(t *testing.T) {
	selfContainerID := "selfcont1234567890abcdef"

	m := &mockClient{
		containerListFn: func(_ context.Context, _ container.ListOptions) ([]container.Summary, error) {
			return []container.Summary{
				{ID: selfContainerID, Names: []string{"/dutd"}, Image: "dutd:latest", ImageID: "sha256:old"},
			}, nil
		},
		imagePullFn: func(_ context.Context, _ string, _ image.PullOptions) (io.ReadCloser, error) {
			return io.NopCloser(strings.NewReader(`{}`)), nil
		},
		imageInspectWithRawFn: func(_ context.Context, _ string) (image.InspectResponse, []byte, error) {
			return image.InspectResponse{
				ID:          "sha256:new",
				RepoDigests: []string{"dutd@sha256:abc123"},
			}, nil, nil
		},
		containerInspectFn: func(_ context.Context, _ string) (container.InspectResponse, error) {
			return container.InspectResponse{
				ContainerJSONBase: &container.ContainerJSONBase{
					Name:       "/dutd",
					HostConfig: &container.HostConfig{},
				},
				Config: &container.Config{
					Image:  "dutd:latest",
					Labels: map[string]string{},
				},
				NetworkSettings: &container.NetworkSettings{
					Networks: map[string]*network.EndpointSettings{
						"bridge": {IPAddress: "172.17.0.2"},
					},
				},
			}, nil
		},
		containerCreateFn: func(_ context.Context, _ *container.Config, _ *container.HostConfig, _ *network.NetworkingConfig, _ *ocispec.Platform, _ string) (container.CreateResponse, error) {
			return container.CreateResponse{ID: "successorid1234567890"}, nil
		},
		containerStartFn: func(_ context.Context, _ string, _ container.StartOptions) error {
			return nil
		},
	}

	cfg := &Config{NameGlobs: []string{"*"}}
	u := New(m, cfg, time.Hour, 30, "selfcont1234", discardLogger())

	shouldExit := u.runOnce(context.Background())
	if !shouldExit {
		t.Error("expected runOnce to return true (self-update triggered)")
	}
}

func TestRunOnce_NoSelfID_NormalBehavior(t *testing.T) {
	// When selfID is empty, all containers go through normal update path.
	sameID := "sha256:same"

	m := &mockClient{
		containerListFn: func(_ context.Context, _ container.ListOptions) ([]container.Summary, error) {
			return []container.Summary{
				{ID: "aaaa1234567890abcdef", Names: []string{"/web"}, Image: "nginx:latest", ImageID: sameID},
			}, nil
		},
		containerInspectFn: func(_ context.Context, _ string) (container.InspectResponse, error) {
			return container.InspectResponse{
				Config: &container.Config{Image: "nginx:latest"},
			}, nil
		},
		imagePullFn: func(_ context.Context, _ string, _ image.PullOptions) (io.ReadCloser, error) {
			return io.NopCloser(strings.NewReader(`{}`)), nil
		},
		imageInspectWithRawFn: func(_ context.Context, _ string) (image.InspectResponse, []byte, error) {
			return image.InspectResponse{
				ID:          sameID,
				RepoDigests: []string{"nginx@sha256:abc123"},
			}, nil, nil
		},
	}

	cfg := &Config{NameGlobs: []string{"*"}}
	u := New(m, cfg, time.Hour, 30, "", discardLogger())

	shouldExit := u.runOnce(context.Background())
	if shouldExit {
		t.Error("expected runOnce to return false (no self-update)")
	}
}

// ---------------------------------------------------------------------------
// shortID
// ---------------------------------------------------------------------------

func TestShortID(t *testing.T) {
	tests := []struct {
		input  string
		expect string
	}{
		{"abcdef1234567890", "abcdef123456"},
		{"short", "short"},
		{"exactly12ch", "exactly12ch"},
		{"exactly12chX", "exactly12chX"},
		{"exactly12chXY", "exactly12chX"},
		{"", ""},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			if got := shortID(tt.input); got != tt.expect {
				t.Errorf("shortID(%q) = %q, want %q", tt.input, got, tt.expect)
			}
		})
	}
}
