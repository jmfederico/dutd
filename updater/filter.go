package updater

import (
	"path"
	"strings"

	"github.com/docker/docker/api/types/container"
)

// Config holds the filter criteria for which containers to update.
type Config struct {
	// NameGlobs is a list of glob patterns matched against the container name
	// (without the leading slash Docker adds internally).
	NameGlobs []string

	// Tags is a list of exact image references (e.g. "nginx:latest",
	// "myregistry.io/app:stable") matched against the container's image name.
	Tags []string
}

// Matches reports whether the given container should be considered a candidate
// for updating. A container matches if it satisfies at least one name glob OR
// at least one tag (union / additive semantics).
func (c *Config) Matches(ct container.Summary) bool {
	name := containerName(ct)

	for _, pattern := range c.NameGlobs {
		ok, err := path.Match(pattern, name)
		if err == nil && ok {
			return true
		}
	}

	for _, tag := range c.Tags {
		if strings.EqualFold(ct.Image, tag) {
			return true
		}
	}

	return false
}

// containerName returns the primary name of a container, stripping the leading
// slash that Docker prepends (e.g. "/nginx" → "nginx").
func containerName(ct container.Summary) string {
	for _, n := range ct.Names {
		name := strings.TrimPrefix(n, "/")
		if name != "" {
			return name
		}
	}
	return ct.ID[:12]
}
