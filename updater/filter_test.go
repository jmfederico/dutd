package updater

import (
	"testing"

	"github.com/docker/docker/api/types/container"
)

func TestConfig_Matches(t *testing.T) {
	tests := []struct {
		name   string
		cfg    Config
		ct     container.Summary
		expect bool
	}{
		{
			name:   "exact name glob match",
			cfg:    Config{NameGlobs: []string{"web-app"}},
			ct:     container.Summary{Names: []string{"/web-app"}, Image: "nginx:latest"},
			expect: true,
		},
		{
			name:   "wildcard name glob match",
			cfg:    Config{NameGlobs: []string{"web-*"}},
			ct:     container.Summary{Names: []string{"/web-frontend"}, Image: "nginx:latest"},
			expect: true,
		},
		{
			name:   "name glob no match",
			cfg:    Config{NameGlobs: []string{"api-*"}},
			ct:     container.Summary{Names: []string{"/web-frontend"}, Image: "nginx:latest"},
			expect: false,
		},
		{
			name:   "exact tag match",
			cfg:    Config{Tags: []string{"nginx:latest"}},
			ct:     container.Summary{Names: []string{"/myapp"}, Image: "nginx:latest"},
			expect: true,
		},
		{
			name:   "tag match is case insensitive",
			cfg:    Config{Tags: []string{"Nginx:Latest"}},
			ct:     container.Summary{Names: []string{"/myapp"}, Image: "nginx:latest"},
			expect: true,
		},
		{
			name:   "tag no match",
			cfg:    Config{Tags: []string{"redis:latest"}},
			ct:     container.Summary{Names: []string{"/myapp"}, Image: "nginx:latest"},
			expect: false,
		},
		{
			name:   "union: name matches but tag does not",
			cfg:    Config{NameGlobs: []string{"my*"}, Tags: []string{"redis:latest"}},
			ct:     container.Summary{Names: []string{"/myapp"}, Image: "nginx:latest"},
			expect: true,
		},
		{
			name:   "union: tag matches but name does not",
			cfg:    Config{NameGlobs: []string{"api-*"}, Tags: []string{"nginx:latest"}},
			ct:     container.Summary{Names: []string{"/web-app"}, Image: "nginx:latest"},
			expect: true,
		},
		{
			name:   "neither matches",
			cfg:    Config{NameGlobs: []string{"api-*"}, Tags: []string{"redis:latest"}},
			ct:     container.Summary{Names: []string{"/web-app"}, Image: "nginx:latest"},
			expect: false,
		},
		{
			name:   "empty config matches nothing",
			cfg:    Config{},
			ct:     container.Summary{Names: []string{"/anything"}, Image: "anything:latest"},
			expect: false,
		},
		{
			name:   "star glob matches everything",
			cfg:    Config{NameGlobs: []string{"*"}},
			ct:     container.Summary{Names: []string{"/whatever"}, Image: "whatever:latest"},
			expect: true,
		},
		{
			name:   "multiple globs - second matches",
			cfg:    Config{NameGlobs: []string{"api-*", "web-*"}},
			ct:     container.Summary{Names: []string{"/web-app"}, Image: "nginx:latest"},
			expect: true,
		},
		{
			name:   "multiple tags - second matches",
			cfg:    Config{Tags: []string{"redis:latest", "nginx:latest"}},
			ct:     container.Summary{Names: []string{"/myapp"}, Image: "nginx:latest"},
			expect: true,
		},
		{
			name:   "question mark glob",
			cfg:    Config{NameGlobs: []string{"app-?"}},
			ct:     container.Summary{Names: []string{"/app-1"}, Image: "myimg:latest"},
			expect: true,
		},
		{
			name:   "question mark glob no match on longer name",
			cfg:    Config{NameGlobs: []string{"app-?"}},
			ct:     container.Summary{Names: []string{"/app-12"}, Image: "myimg:latest"},
			expect: false,
		},
		// Label filter tests.
		{
			name:   "label key=value match",
			cfg:    Config{Labels: []string{"com.example.update=true"}},
			ct:     container.Summary{Names: []string{"/myapp"}, Image: "nginx:latest", Labels: map[string]string{"com.example.update": "true"}},
			expect: true,
		},
		{
			name:   "label key=value no match on wrong value",
			cfg:    Config{Labels: []string{"com.example.update=true"}},
			ct:     container.Summary{Names: []string{"/myapp"}, Image: "nginx:latest", Labels: map[string]string{"com.example.update": "false"}},
			expect: false,
		},
		{
			name:   "label key=value no match on missing key",
			cfg:    Config{Labels: []string{"com.example.update=true"}},
			ct:     container.Summary{Names: []string{"/myapp"}, Image: "nginx:latest", Labels: map[string]string{}},
			expect: false,
		},
		{
			name:   "label key-only match (any value)",
			cfg:    Config{Labels: []string{"com.example.update"}},
			ct:     container.Summary{Names: []string{"/myapp"}, Image: "nginx:latest", Labels: map[string]string{"com.example.update": "anything"}},
			expect: true,
		},
		{
			name:   "label key-only match with empty value",
			cfg:    Config{Labels: []string{"com.example.update"}},
			ct:     container.Summary{Names: []string{"/myapp"}, Image: "nginx:latest", Labels: map[string]string{"com.example.update": ""}},
			expect: true,
		},
		{
			name:   "label key-only no match on missing key",
			cfg:    Config{Labels: []string{"com.example.update"}},
			ct:     container.Summary{Names: []string{"/myapp"}, Image: "nginx:latest", Labels: map[string]string{"other": "value"}},
			expect: false,
		},
		{
			name:   "label match with nil labels on container",
			cfg:    Config{Labels: []string{"com.example.update"}},
			ct:     container.Summary{Names: []string{"/myapp"}, Image: "nginx:latest"},
			expect: false,
		},
		{
			name:   "label key=empty-string matches label with empty value",
			cfg:    Config{Labels: []string{"marker="}},
			ct:     container.Summary{Names: []string{"/myapp"}, Image: "nginx:latest", Labels: map[string]string{"marker": ""}},
			expect: true,
		},
		{
			name:   "label key=empty-string does not match label with non-empty value",
			cfg:    Config{Labels: []string{"marker="}},
			ct:     container.Summary{Names: []string{"/myapp"}, Image: "nginx:latest", Labels: map[string]string{"marker": "something"}},
			expect: false,
		},
		{
			name:   "multiple labels - second matches",
			cfg:    Config{Labels: []string{"missing", "present=yes"}},
			ct:     container.Summary{Names: []string{"/myapp"}, Image: "nginx:latest", Labels: map[string]string{"present": "yes"}},
			expect: true,
		},
		{
			name:   "union: label matches but name and tag do not",
			cfg:    Config{NameGlobs: []string{"api-*"}, Tags: []string{"redis:latest"}, Labels: []string{"com.example.update=true"}},
			ct:     container.Summary{Names: []string{"/web-app"}, Image: "nginx:latest", Labels: map[string]string{"com.example.update": "true"}},
			expect: true,
		},
		{
			name:   "union: name matches but label does not",
			cfg:    Config{NameGlobs: []string{"web-*"}, Labels: []string{"missing"}},
			ct:     container.Summary{Names: []string{"/web-app"}, Image: "nginx:latest", Labels: map[string]string{}},
			expect: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.cfg.Matches(tt.ct)
			if got != tt.expect {
				t.Errorf("Config.Matches() = %v, want %v", got, tt.expect)
			}
		})
	}
}

func TestContainerName(t *testing.T) {
	tests := []struct {
		name   string
		ct     container.Summary
		expect string
	}{
		{
			name:   "strips leading slash",
			ct:     container.Summary{Names: []string{"/web-app"}},
			expect: "web-app",
		},
		{
			name:   "returns first non-empty name",
			ct:     container.Summary{Names: []string{"/first", "/second"}},
			expect: "first",
		},
		{
			name:   "falls back to ID when no names",
			ct:     container.Summary{ID: "abcdef1234567890", Names: []string{}},
			expect: "abcdef123456",
		},
		{
			name:   "name without leading slash",
			ct:     container.Summary{Names: []string{"no-slash"}},
			expect: "no-slash",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := containerName(tt.ct)
			if got != tt.expect {
				t.Errorf("containerName() = %q, want %q", got, tt.expect)
			}
		})
	}
}
