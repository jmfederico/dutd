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
