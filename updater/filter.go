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

	// Labels is a list of label filters in "key=value" or "key" form.
	// A "key=value" entry matches containers whose label with the given key
	// has exactly the given value. A "key" entry (no '=') matches containers
	// that have the label regardless of its value.
	Labels []string
}

// Matches reports whether the given container should be considered a candidate
// for updating. A container matches if it satisfies at least one name glob OR
// at least one tag OR at least one label filter (union / additive semantics).
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

	for _, lf := range c.Labels {
		if matchLabel(ct.Labels, lf) {
			return true
		}
	}

	return false
}

// matchLabel checks whether the container's labels satisfy a single filter.
// The filter is either "key=value" (exact match on both key and value) or
// "key" (the label must exist, any value).
func matchLabel(labels map[string]string, filter string) bool {
	if k, v, ok := strings.Cut(filter, "="); ok {
		return labels[k] == v
	}
	_, exists := labels[filter]
	return exists
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
