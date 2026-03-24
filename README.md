# dutd — Docker Up To Date

A lightweight, long-running daemon that keeps Docker containers up to date.

dutd periodically checks running containers, pulls the latest version of their images, and recreates any container whose image has changed. Containers whose image is already current are left untouched.

## Features

- Single static binary (~6 MB), tiny Docker image (~4 MB compressed, ~16 MB on disk), no runtime dependencies
- Keeps itself up to date automatically when running as a container
- Connects to Docker via Unix socket only (no TCP/TLS)
- Configurable check interval
- Filter containers by name (glob patterns), image tag (exact match), or label, or any combination
- Preserves full container config on recreate: env, mounts, ports, networks, labels, restart policy, entrypoint, user
- Skips restart when the pulled image is identical to the running one
- Graceful shutdown via `docker stop` with configurable timeout
- Structured JSON logs to stdout
- Runs on Linux amd64 and arm64

## Quick start

### Docker Run

```bash
docker run -d \
  --name dutd \
  --restart unless-stopped \
  --user "$(id -u):$(getent group docker | cut -d: -f3)" \
  -v /var/run/docker.sock:/var/run/docker.sock \
  ghcr.io/jmfederico/dutd:latest \
  -interval=1h -label=com.example.dutd=true
```

### Docker Compose

```yaml
services:
  dutd:
    image: ghcr.io/jmfederico/dutd:latest
    restart: unless-stopped
    user: "${UID}:${DOCKER_GID}"
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock
    command:
      - -interval=1h
      - -label=com.example.dutd=true

  web:
    image: nginx:latest
    labels:
      com.example.dutd: "true"
```

Set `UID` and `DOCKER_GID` in your `.env` file or export them before running:

```bash
echo "UID=$(id -u)" >> .env
echo "DOCKER_GID=$(getent group docker | cut -d: -f3)" >> .env
docker compose up -d
```

### SELinux

On systems with SELinux enforcing (e.g. Fedora, RHEL, CentOS), the container may be denied access to the Docker socket even with the correct user and group. The typical symptom is a log entry like:

```
docker socket not reachable  socket=/var/run/docker.sock  err="permission denied while trying to connect to the Docker daemon socket at unix:///var/run/docker.sock"
```

Add `--security-opt label=disable` to allow it:

```bash
docker run -d \
  --name dutd \
  --restart unless-stopped \
  --user "$(id -u):$(getent group docker | cut -d: -f3)" \
  --security-opt label=disable \
  -v /var/run/docker.sock:/var/run/docker.sock \
  ghcr.io/jmfederico/dutd:latest \
  -interval=1h -label=com.example.dutd=true
```

For Docker Compose, add the equivalent under the service:

```yaml
services:
  dutd:
    image: ghcr.io/jmfederico/dutd:latest
    restart: unless-stopped
    user: "${UID}:${DOCKER_GID}"
    security_opt:
      - label=disable
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock
    command:
      - -interval=1h
      - -label=com.example.dutd=true
```

### Binary

```
dutd -interval 1h -name "*"
```

## Usage

```
dutd [flags]

  -socket        Path to Docker socket (default: /var/run/docker.sock)
  -interval      Check interval (default: 1h, e.g. 30m, 6h, 24h)
  -stop-timeout  Seconds to wait for graceful stop (default: 30)
  -name          Glob pattern to match container names (repeatable)
  -tag           Exact image reference to match (repeatable)
  -label         Label filter as "key=value" or "key" (repeatable)
```

At least one `-name`, `-tag`, or `-label` is required. Filters are additive: a container is updated if it matches **any** `-name` glob, **any** `-tag`, **or** any `-label`.

### Examples

```bash
# Update containers named web-* or api-* every 30 minutes
dutd -interval 30m -name "web-*" -name "api-*"

# Update specific images every hour
dutd -tag nginx:latest -tag redis:latest

# Update everything every 6 hours
dutd -interval 6h -name "*"

# Update containers with a specific label value
dutd -label com.example.dutd=true

# Update containers that have a label (any value)
dutd -label com.example.dutd
```

## Building

```bash
# Local binary
go build -o dutd .

# Docker image (multi-arch)
docker buildx build --platform linux/amd64,linux/arm64 -t dutd:latest .
```

## How it works

1. List all running containers
2. Filter by `-name` globs, `-tag` values, and `-label` filters
3. For each matching container, pull the image
4. Compare the pulled image ID to the running container's image ID
5. If unchanged, skip
6. If different: snapshot config, stop, remove, recreate with the new image
7. Sleep until the next interval

If a check is still running when the next interval fires, the tick is skipped.

## Testing

```bash
# Unit tests (no Docker required)
go test ./...

# Integration tests (requires a Docker socket)
go test -tags integration ./updater/ -v
```

## Limitations

- Socket mode only. No TCP/TLS Docker endpoints.
- Compose-managed containers are treated as plain containers. Compose state will diverge after an update.
- No rollback if the new container fails to start.
- No registry authentication (pull works for public images and registries where the Docker daemon is already logged in).
- No automatic cleanup of old dangling images.

## License

MIT
