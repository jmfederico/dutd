package docker

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/docker/docker/client"
)

// NewSocketClient creates a Docker client that connects exclusively via Unix socket.
// No TCP/TLS endpoint is ever used.
func NewSocketClient(socketPath string) (*client.Client, error) {
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			d := &net.Dialer{Timeout: 10 * time.Second}
			return d.DialContext(ctx, "unix", socketPath)
		},
	}

	httpClient := &http.Client{Transport: transport}

	cli, err := client.NewClientWithOpts(
		client.WithHost("unix://"+socketPath),
		client.WithHTTPClient(httpClient),
		client.WithAPIVersionNegotiation(),
	)
	if err != nil {
		return nil, fmt.Errorf("create docker client: %w", err)
	}

	return cli, nil
}
