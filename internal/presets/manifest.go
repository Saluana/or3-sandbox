package presets

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"or3-sandbox/internal/model"
)

// ManifestFileName is the canonical filename for preset manifests under the
// examples tree.
const ManifestFileName = "preset.yaml"

// Manifest is the top-level preset manifest schema.
type Manifest struct {
	Name        string          `json:"name" yaml:"name"`
	Description string          `json:"description,omitempty" yaml:"description,omitempty"`
	Runtime     RuntimeSelector `json:"runtime,omitempty" yaml:"runtime,omitempty"`
	Sandbox     SandboxPreset   `json:"sandbox" yaml:"sandbox"`
	Inputs      []Input         `json:"inputs,omitempty" yaml:"inputs,omitempty"`
	Files       []FileAsset     `json:"files,omitempty" yaml:"files,omitempty"`
	Bootstrap   []Step          `json:"bootstrap,omitempty" yaml:"bootstrap,omitempty"`
	Startup     *Step           `json:"startup,omitempty" yaml:"startup,omitempty"`
	Readiness   *ReadinessCheck `json:"readiness,omitempty" yaml:"readiness,omitempty"`
	Tunnel      *Tunnel         `json:"tunnel,omitempty" yaml:"tunnel,omitempty"`
	Artifacts   []Artifact      `json:"artifacts,omitempty" yaml:"artifacts,omitempty"`
	Cleanup     CleanupPolicy   `json:"cleanup,omitempty" yaml:"cleanup,omitempty"`

	BaseDir string `json:"-" yaml:"-"`
}

// RuntimeSelector constrains which runtimes a preset may use.
type RuntimeSelector struct {
	Allowed []string `json:"allowed,omitempty" yaml:"allowed,omitempty"`
	Profile string   `json:"profile,omitempty" yaml:"profile,omitempty"`
}

// SandboxPreset describes the sandbox to create before running steps.
type SandboxPreset struct {
	Image        string `json:"image" yaml:"image"`
	CPULimit     string `json:"cpu,omitempty" yaml:"cpu,omitempty"`
	MemoryMB     int    `json:"memory_mb,omitempty" yaml:"memory_mb,omitempty"`
	PIDsLimit    int    `json:"pids,omitempty" yaml:"pids,omitempty"`
	DiskMB       int    `json:"disk_mb,omitempty" yaml:"disk_mb,omitempty"`
	NetworkMode  string `json:"network,omitempty" yaml:"network,omitempty"`
	AllowTunnels bool   `json:"allow_tunnels,omitempty" yaml:"allow_tunnels,omitempty"`
	Start        *bool  `json:"start,omitempty" yaml:"start,omitempty"`
}

// Input declares a user-supplied preset parameter.
type Input struct {
	Name        string `json:"name" yaml:"name"`
	Required    bool   `json:"required,omitempty" yaml:"required,omitempty"`
	Secret      bool   `json:"secret,omitempty" yaml:"secret,omitempty"`
	Description string `json:"description,omitempty" yaml:"description,omitempty"`
	Default     string `json:"default,omitempty" yaml:"default,omitempty"`
}

// FileAsset writes inline or source-backed content into the sandbox.
type FileAsset struct {
	Path    string `json:"path" yaml:"path"`
	Content string `json:"content,omitempty" yaml:"content,omitempty"`
	Source  string `json:"source,omitempty" yaml:"source,omitempty"`
	Binary  bool   `json:"binary,omitempty" yaml:"binary,omitempty"`
}

// Step describes a command executed during bootstrap or startup.
type Step struct {
	Name            string            `json:"name,omitempty" yaml:"name,omitempty"`
	Command         []string          `json:"command" yaml:"command"`
	Env             map[string]string `json:"env,omitempty" yaml:"env,omitempty"`
	Cwd             string            `json:"cwd,omitempty" yaml:"cwd,omitempty"`
	Timeout         time.Duration     `json:"timeout,omitempty" yaml:"timeout,omitempty"`
	Detached        bool              `json:"detached,omitempty" yaml:"detached,omitempty"`
	ContinueOnError bool              `json:"continue_on_error,omitempty" yaml:"continue_on_error,omitempty"`
}

// ReadinessCheck defines how a preset waits for the workload to become ready.
type ReadinessCheck struct {
	Type           string        `json:"type" yaml:"type"`
	Command        []string      `json:"command,omitempty" yaml:"command,omitempty"`
	Path           string        `json:"path,omitempty" yaml:"path,omitempty"`
	Port           int           `json:"port,omitempty" yaml:"port,omitempty"`
	ExpectedStatus int           `json:"expected_status,omitempty" yaml:"expected_status,omitempty"`
	Timeout        time.Duration `json:"timeout,omitempty" yaml:"timeout,omitempty"`
	Interval       time.Duration `json:"interval,omitempty" yaml:"interval,omitempty"`
}

// Tunnel configures an optional published tunnel for the preset workload.
type Tunnel struct {
	Port       int    `json:"port" yaml:"port"`
	Protocol   string `json:"protocol,omitempty" yaml:"protocol,omitempty"`
	AuthMode   string `json:"auth_mode,omitempty" yaml:"auth_mode,omitempty"`
	Visibility string `json:"visibility,omitempty" yaml:"visibility,omitempty"`
}

// Artifact describes a file copied back from the sandbox after execution.
type Artifact struct {
	RemotePath string `json:"remote_path" yaml:"remote_path"`
	LocalPath  string `json:"local_path" yaml:"local_path"`
	Binary     bool   `json:"binary,omitempty" yaml:"binary,omitempty"`
}

// CleanupPolicy controls when preset-created sandboxes are deleted.
type CleanupPolicy string

const (
	// CleanupOnSuccess removes the sandbox only when the preset run succeeds.
	CleanupOnSuccess CleanupPolicy = "on-success"
	// CleanupAlways removes the sandbox regardless of run outcome.
	CleanupAlways CleanupPolicy = "always"
	// CleanupNever keeps the sandbox after the preset run finishes.
	CleanupNever CleanupPolicy = "never"
)

// Normalize fills in implied defaults so the manifest can be executed
// consistently.
func (m *Manifest) Normalize() {
	if strings.TrimSpace(m.Name) == "" && strings.TrimSpace(m.BaseDir) != "" {
		m.Name = filepath.Base(m.BaseDir)
	}
	if m.Sandbox.CPULimit == "" {
		m.Sandbox.CPULimit = "1"
	}
	if m.Sandbox.MemoryMB <= 0 {
		m.Sandbox.MemoryMB = 1024
	}
	if m.Sandbox.PIDsLimit <= 0 {
		m.Sandbox.PIDsLimit = 512
	}
	if m.Sandbox.DiskMB <= 0 {
		m.Sandbox.DiskMB = 4096
	}
	if strings.TrimSpace(m.Sandbox.NetworkMode) == "" {
		m.Sandbox.NetworkMode = "internet-enabled"
	}
	if m.Cleanup == "" {
		m.Cleanup = CleanupOnSuccess
	}
	for index := range m.Bootstrap {
		normalizeStep(&m.Bootstrap[index], fmt.Sprintf("bootstrap[%d]", index))
	}
	if m.Startup != nil {
		normalizeStep(m.Startup, "startup")
	}
	if m.Readiness != nil {
		if m.Readiness.Timeout <= 0 {
			m.Readiness.Timeout = 30 * time.Second
		}
		if m.Readiness.Interval <= 0 {
			m.Readiness.Interval = time.Second
		}
		if m.Readiness.ExpectedStatus == 0 {
			m.Readiness.ExpectedStatus = 200
		}
		if m.Readiness.Path == "" {
			m.Readiness.Path = "/"
		}
	}
	if m.Tunnel != nil {
		if m.Tunnel.Protocol == "" {
			m.Tunnel.Protocol = "http"
		}
		if m.Tunnel.AuthMode == "" {
			m.Tunnel.AuthMode = "token"
		}
		if m.Tunnel.Visibility == "" {
			m.Tunnel.Visibility = "private"
		}
	}
	for index := range m.Runtime.Allowed {
		m.Runtime.Allowed[index] = strings.ToLower(strings.TrimSpace(m.Runtime.Allowed[index]))
	}
	sort.Strings(m.Runtime.Allowed)
}

func normalizeStep(step *Step, fallbackName string) {
	if step.Name == "" {
		step.Name = fallbackName
	}
	if step.Timeout <= 0 {
		step.Timeout = 5 * time.Minute
	}
	if step.Cwd == "" {
		step.Cwd = "/workspace"
	}
}

// Validate checks manifest invariants and returns an error for invalid or
// ambiguous configuration.
func (m Manifest) Validate() error {
	if strings.TrimSpace(m.Name) == "" {
		return fmt.Errorf("name is required")
	}
	if strings.TrimSpace(m.Sandbox.Image) == "" {
		return fmt.Errorf("sandbox.image is required")
	}
	if len(m.Runtime.Allowed) > 0 {
		seen := map[string]struct{}{}
		for _, runtimeName := range m.Runtime.Allowed {
			if runtimeName == "" {
				return fmt.Errorf("runtime.allowed entries must not be empty")
			}
			if _, exists := seen[runtimeName]; exists {
				return fmt.Errorf("runtime.allowed contains duplicate value %q", runtimeName)
			}
			seen[runtimeName] = struct{}{}
		}
	}
	if profile := model.GuestProfile(strings.TrimSpace(m.Runtime.Profile)); m.Runtime.Profile != "" && !profile.IsValid() {
		return fmt.Errorf("runtime.profile %q is invalid", m.Runtime.Profile)
	}
	seenInputs := map[string]struct{}{}
	for _, input := range m.Inputs {
		name := strings.TrimSpace(input.Name)
		if name == "" {
			return fmt.Errorf("input name is required")
		}
		if _, exists := seenInputs[name]; exists {
			return fmt.Errorf("duplicate input name %q", name)
		}
		seenInputs[name] = struct{}{}
	}
	for _, file := range m.Files {
		if strings.TrimSpace(file.Path) == "" {
			return fmt.Errorf("file path is required")
		}
		hasContent := strings.TrimSpace(file.Content) != ""
		hasSource := strings.TrimSpace(file.Source) != ""
		if hasContent == hasSource {
			return fmt.Errorf("file %q must specify exactly one of content or source", file.Path)
		}
	}
	for index, step := range m.Bootstrap {
		if len(step.Command) == 0 {
			return fmt.Errorf("bootstrap[%d] command is required", index)
		}
	}
	if m.Startup != nil && len(m.Startup.Command) == 0 {
		return fmt.Errorf("startup command is required")
	}
	if m.Readiness != nil {
		switch strings.ToLower(strings.TrimSpace(m.Readiness.Type)) {
		case "command":
			if len(m.Readiness.Command) == 0 {
				return fmt.Errorf("readiness.command requires command")
			}
		case "http":
			if m.Tunnel == nil {
				return fmt.Errorf("readiness.http requires tunnel configuration")
			}
			if m.Tunnel.Port <= 0 {
				return fmt.Errorf("tunnel.port must be positive for readiness.http")
			}
		case "":
			return fmt.Errorf("readiness.type is required")
		default:
			return fmt.Errorf("unsupported readiness.type %q", m.Readiness.Type)
		}
	}
	if m.Tunnel != nil {
		if m.Tunnel.Port <= 0 {
			return fmt.Errorf("tunnel.port must be positive")
		}
	}
	for _, artifact := range m.Artifacts {
		if strings.TrimSpace(artifact.RemotePath) == "" || strings.TrimSpace(artifact.LocalPath) == "" {
			return fmt.Errorf("artifacts require remote_path and local_path")
		}
	}
	switch m.Cleanup {
	case CleanupOnSuccess, CleanupAlways, CleanupNever:
	default:
		return fmt.Errorf("unsupported cleanup policy %q", m.Cleanup)
	}
	return nil
}

// AllowsRuntime reports whether runtimeName is allowed by the manifest.
func (m Manifest) AllowsRuntime(runtimeName string) bool {
	if len(m.Runtime.Allowed) == 0 {
		return true
	}
	runtimeName = strings.ToLower(strings.TrimSpace(runtimeName))
	for _, allowed := range m.Runtime.Allowed {
		if allowed == runtimeName {
			return true
		}
	}
	return false
}
