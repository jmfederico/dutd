//go:build integration

package updater

import (
	"context"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

const (
	// Two distinct, small alpine images guaranteed to have different image IDs.
	oldImage = "alpine:3.19"
	newImage = "alpine:3.20"

	testContainerPrefix = "dutd-integ-"
)

// integrationClient creates a real Docker client connected via the default
// Unix socket, or skips the test if the socket is not available.
func integrationClient(t *testing.T) *client.Client {
	t.Helper()

	socket := "/var/run/docker.sock"
	if s := os.Getenv("DOCKER_SOCKET"); s != "" {
		socket = s
	}

	cli, err := client.NewClientWithOpts(
		client.WithHost("unix://"+socket),
		client.WithAPIVersionNegotiation(),
	)
	if err != nil {
		t.Skipf("cannot create docker client: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := cli.Ping(ctx); err != nil {
		t.Skipf("docker not reachable: %v", err)
	}

	return cli
}

// pullOrSkip pulls an image or skips the test.
func pullOrSkip(t *testing.T, cli *client.Client, ref string) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	rc, err := cli.ImagePull(ctx, ref, image.PullOptions{})
	if err != nil {
		t.Skipf("cannot pull %s: %v", ref, err)
	}
	defer rc.Close()
	io.Copy(io.Discard, rc)
}

// forceRemoveByName removes any existing container with the given name.
// This handles leftovers from previous interrupted test runs.
func forceRemoveByName(t *testing.T, cli *client.Client, name string) {
	t.Helper()
	ctx := context.Background()
	resp, err := cli.ContainerInspect(ctx, name)
	if err != nil {
		return // container doesn't exist — nothing to do
	}
	timeout := 3
	_ = cli.ContainerStop(ctx, resp.ID, container.StopOptions{Timeout: &timeout})
	_ = cli.ContainerRemove(ctx, resp.ID, container.RemoveOptions{Force: true})
	t.Logf("cleaned up stale container %s (%s)", name, resp.ID[:12])
}

// runTestContainer creates and starts a container with the given name and
// image, registering a cleanup function to force-remove it. If a container
// with the same name already exists (leftover from a previous interrupted
// run), it is force-removed first.
func runTestContainer(t *testing.T, cli *client.Client, name, img string, env []string, labels map[string]string, portBindings map[string][]portBinding, restartPolicy container.RestartPolicy) string {
	t.Helper()

	forceRemoveByName(t, cli, name)

	cfg := &container.Config{
		Image:  img,
		Cmd:    []string{"sleep", "3600"},
		Env:    env,
		Labels: labels,
	}

	hc := &container.HostConfig{
		RestartPolicy: restartPolicy,
	}

	if len(portBindings) > 0 {
		portMap := make(nat.PortMap)
		for port, bindings := range portBindings {
			for _, b := range bindings {
				portMap[nat.Port(port)] = append(portMap[nat.Port(port)], nat.PortBinding{
					HostIP:   b.HostIP,
					HostPort: b.HostPort,
				})
			}
		}
		hc.PortBindings = portMap
	}

	ctx := context.Background()

	resp, err := cli.ContainerCreate(ctx, cfg, hc, nil, nil, name)
	if err != nil {
		t.Fatalf("create container %s: %v", name, err)
	}

	t.Cleanup(func() {
		timeout := 5
		_ = cli.ContainerStop(context.Background(), resp.ID, container.StopOptions{Timeout: &timeout})
		_ = cli.ContainerRemove(context.Background(), resp.ID, container.RemoveOptions{Force: true})
	})

	if err := cli.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		t.Fatalf("start container %s: %v", name, err)
	}

	return resp.ID
}

type portBinding struct {
	HostIP   string
	HostPort string
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

// ---------------------------------------------------------------------------
// Test 1: No update needed — same image ID after pull
// ---------------------------------------------------------------------------

func TestIntegration_NoUpdateNeeded(t *testing.T) {
	cli := integrationClient(t)
	defer cli.Close()

	pullOrSkip(t, cli, oldImage)

	name := testContainerPrefix + "noop"
	originalID := runTestContainer(t, cli, name, oldImage, nil, nil, nil, container.RestartPolicy{})

	// Build a Summary as the updater would see it from ContainerList.
	info, err := cli.ContainerInspect(context.Background(), originalID)
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}

	ct := container.Summary{
		ID:      originalID,
		Names:   []string{"/" + name},
		Image:   oldImage,
		ImageID: info.Image,
	}

	cfg := &Config{NameGlobs: []string{name}}
	u := New(cli, cfg, time.Hour, 10, "", testLogger())

	// updateContainer will pull alpine:3.19 again — same image ID — skip.
	updated, err := u.updateContainer(context.Background(), ct)
	if err != nil {
		t.Fatalf("updateContainer: %v", err)
	}
	if updated {
		t.Error("container should NOT have been updated (same image)")
	}

	// Verify the container is still running with the same ID.
	afterInfo, err := cli.ContainerInspect(context.Background(), originalID)
	if err != nil {
		t.Fatalf("container disappeared: %v", err)
	}
	if !afterInfo.State.Running {
		t.Error("container should still be running")
	}
}

// ---------------------------------------------------------------------------
// Test 2: Update lifecycle — snapshot, stop, remove, recreate with new image
// ---------------------------------------------------------------------------

func TestIntegration_UpdateLifecycle(t *testing.T) {
	cli := integrationClient(t)
	defer cli.Close()

	pullOrSkip(t, cli, oldImage)
	pullOrSkip(t, cli, newImage)

	ctx := context.Background()
	name := testContainerPrefix + "update"
	originalID := runTestContainer(t, cli, name, oldImage, nil, nil, nil, container.RestartPolicy{})

	log := testLogger()

	// Snapshot the running container's config.
	snap, err := snapshotContainer(ctx, cli, originalID)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}

	// Stop and remove the container.
	if err := stopAndRemove(ctx, cli, originalID, 10, log); err != nil {
		t.Fatalf("stopAndRemove: %v", err)
	}

	// Recreate with the new (different) image.
	if err := recreate(ctx, cli, snap, newImage, log); err != nil {
		t.Fatalf("recreate: %v", err)
	}

	// The old container should be gone.
	_, err = cli.ContainerInspect(ctx, originalID)
	if err == nil {
		t.Error("old container should have been removed")
	}

	// A new container with the same name should exist and be running.
	newInfo, err := cli.ContainerInspect(ctx, name)
	if err != nil {
		t.Fatalf("new container not found: %v", err)
	}

	t.Cleanup(func() {
		timeout := 5
		_ = cli.ContainerStop(context.Background(), newInfo.ID, container.StopOptions{Timeout: &timeout})
		_ = cli.ContainerRemove(context.Background(), newInfo.ID, container.RemoveOptions{Force: true})
	})

	if newInfo.ID == originalID {
		t.Error("new container has the same ID — it wasn't recreated")
	}
	if !newInfo.State.Running {
		t.Error("new container should be running")
	}

	// Verify the image was actually changed.
	if newInfo.Config.Image != newImage {
		t.Errorf("image = %q, want %q", newInfo.Config.Image, newImage)
	}
}

// ---------------------------------------------------------------------------
// Test 3: Config preservation — verify env, labels, ports, restart policy
//         survive the snapshot → stop → remove → recreate cycle
// ---------------------------------------------------------------------------

func TestIntegration_ConfigPreservation(t *testing.T) {
	cli := integrationClient(t)
	defer cli.Close()

	pullOrSkip(t, cli, oldImage)
	pullOrSkip(t, cli, newImage)

	ctx := context.Background()
	name := testContainerPrefix + "config"

	expectedEnv := []string{"FOO=bar", "BAZ=qux"}
	expectedLabels := map[string]string{"test.dutd": "true", "test.purpose": "config-preservation"}
	expectedRestart := container.RestartPolicy{Name: container.RestartPolicyUnlessStopped}

	originalID := runTestContainer(t, cli, name, oldImage,
		expectedEnv,
		expectedLabels,
		map[string][]portBinding{
			"80/tcp": {{HostIP: "0.0.0.0", HostPort: "18765"}},
		},
		expectedRestart,
	)

	log := testLogger()

	// Snapshot the full config.
	snap, err := snapshotContainer(ctx, cli, originalID)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}

	// Stop and remove.
	if err := stopAndRemove(ctx, cli, originalID, 10, log); err != nil {
		t.Fatalf("stopAndRemove: %v", err)
	}

	// Recreate with a different image to prove the image changed while config
	// is preserved.
	if err := recreate(ctx, cli, snap, newImage, log); err != nil {
		t.Fatalf("recreate: %v", err)
	}

	// Find the new container.
	newInfo, err := cli.ContainerInspect(ctx, name)
	if err != nil {
		t.Fatalf("new container not found: %v", err)
	}

	newID := newInfo.ID
	t.Cleanup(func() {
		timeout := 5
		_ = cli.ContainerStop(context.Background(), newID, container.StopOptions{Timeout: &timeout})
		_ = cli.ContainerRemove(context.Background(), newID, container.RemoveOptions{Force: true})
	})

	// --- Different container ID (actually recreated) ---
	if newID == originalID {
		t.Error("new container has the same ID — it wasn't recreated")
	}

	// --- Container is running ---
	if !newInfo.State.Running {
		t.Error("new container should be running")
	}

	// --- Image changed ---
	if newInfo.Config.Image != newImage {
		t.Errorf("image = %q, want %q", newInfo.Config.Image, newImage)
	}

	// --- Environment variables ---
	for _, expected := range expectedEnv {
		found := false
		for _, actual := range newInfo.Config.Env {
			if actual == expected {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("env var %q not found in new container (has: %v)", expected, newInfo.Config.Env)
		}
	}

	// --- Labels ---
	for k, v := range expectedLabels {
		actual, ok := newInfo.Config.Labels[k]
		if !ok {
			t.Errorf("label %q not found in new container", k)
		} else if actual != v {
			t.Errorf("label %q = %q, want %q", k, actual, v)
		}
	}

	// --- Restart policy ---
	if newInfo.HostConfig.RestartPolicy.Name != expectedRestart.Name {
		t.Errorf("restart policy = %q, want %q", newInfo.HostConfig.RestartPolicy.Name, expectedRestart.Name)
	}

	// --- Port bindings ---
	bindings, ok := newInfo.HostConfig.PortBindings[nat.Port("80/tcp")]
	if !ok {
		t.Error("port binding 80/tcp not found in new container")
	} else if len(bindings) == 0 {
		t.Error("port binding 80/tcp has no entries")
	} else if bindings[0].HostPort != "18765" {
		t.Errorf("port binding host port = %q, want %q", bindings[0].HostPort, "18765")
	}

	// --- Container name ---
	newName := strings.TrimPrefix(newInfo.Name, "/")
	if newName != name {
		t.Errorf("container name = %q, want %q", newName, name)
	}

	// --- Network membership ---
	if len(newInfo.NetworkSettings.Networks) == 0 {
		t.Error("new container has no networks")
	}
}
