package dockerimage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"sort"
	"strconv"
	"strings"

	"or3-sandbox/internal/model"
)

const (
	LabelProfile      = "org.or3.profile"
	LabelCapabilities = "org.or3.capabilities"
	LabelDangerous    = "org.or3.dangerous"
)

type Metadata struct {
	Ref          string
	Profile      model.GuestProfile
	Capabilities []string
	Dangerous    bool
}

type rule struct {
	repository   string
	profile      model.GuestProfile
	capabilities []string
	dangerous    bool
}

var curatedRules = []rule{
	{repository: "alpine", profile: model.GuestProfileCore},
	{repository: "busybox", profile: model.GuestProfileCore},
	{repository: "debian", profile: model.GuestProfileRuntime},
	{repository: "ubuntu", profile: model.GuestProfileRuntime},
	{repository: "node", profile: model.GuestProfileRuntime},
	{repository: "python", profile: model.GuestProfileRuntime},
	{repository: "ghcr.io/openclaw/openclaw", profile: model.GuestProfileRuntime},
	{repository: "or3-sandbox/base", profile: model.GuestProfileRuntime},
	{repository: "ghcr.io/or3-sandbox/base", profile: model.GuestProfileRuntime},
	{repository: "or3-sandbox/base-browser", profile: model.GuestProfileBrowser, capabilities: []string{"browser"}},
	{repository: "ghcr.io/or3-sandbox/base-browser", profile: model.GuestProfileBrowser, capabilities: []string{"browser"}},
	{repository: "or3-sandbox/base-container", profile: model.GuestProfileContainer, capabilities: []string{"inner-docker"}, dangerous: true},
	{repository: "ghcr.io/or3-sandbox/base-container", profile: model.GuestProfileContainer, capabilities: []string{"inner-docker"}, dangerous: true},
	{repository: "or3-sandbox/base-debug", profile: model.GuestProfileDebug, dangerous: true},
	{repository: "ghcr.io/or3-sandbox/base-debug", profile: model.GuestProfileDebug, dangerous: true},
	{repository: "mcr.microsoft.com/playwright", profile: model.GuestProfileBrowser, capabilities: []string{"browser"}},
}

var ErrMetadataUnavailable = errors.New("docker image metadata unavailable")

type LabelProvider func(context.Context, string) (map[string]string, error)

func Resolve(ref string) (Metadata, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return Metadata{}, fmt.Errorf("docker image reference is required")
	}
	if metadata, ok := resolveCurated(ref); ok {
		return metadata, nil
	}
	return Metadata{}, missingMetadataError(ref)
}

func ResolveWithLabelProvider(ctx context.Context, ref string, provider LabelProvider) (Metadata, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return Metadata{}, fmt.Errorf("docker image reference is required")
	}
	if metadata, ok := resolveCurated(ref); ok {
		return metadata, nil
	}
	if provider != nil {
		labels, err := provider(ctx, ref)
		if err != nil {
			return Metadata{}, err
		}
		if len(labels) > 0 {
			return ParseLabels(ref, labels)
		}
	}
	return Metadata{}, missingMetadataError(ref)
}

func ResolveWithDockerLabels(ctx context.Context, ref string) (Metadata, error) {
	return ResolveWithLabelProvider(ctx, ref, DockerLabelProvider("docker"))
}

func ParseLabels(ref string, labels map[string]string) (Metadata, error) {
	profile := model.GuestProfile(strings.ToLower(strings.TrimSpace(labels[LabelProfile])))
	if !profile.IsValid() {
		return Metadata{}, fmt.Errorf("docker image %q label %s must be one of the curated guest profiles", ref, LabelProfile)
	}
	dangerous := false
	if raw := strings.TrimSpace(labels[LabelDangerous]); raw != "" {
		parsed, err := strconv.ParseBool(raw)
		if err != nil {
			return Metadata{}, fmt.Errorf("docker image %q label %s must be a boolean", ref, LabelDangerous)
		}
		dangerous = parsed
	}
	return Metadata{
		Ref:          strings.TrimSpace(ref),
		Profile:      profile,
		Capabilities: parseCapabilities(labels[LabelCapabilities]),
		Dangerous:    dangerous,
	}, nil
}

func parseCapabilities(raw string) []string {
	entries := strings.Split(raw, ",")
	seen := make(map[string]struct{}, len(entries))
	result := make([]string, 0, len(entries))
	for _, entry := range entries {
		value := strings.ToLower(strings.TrimSpace(entry))
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	if len(result) == 0 {
		return nil
	}
	return result
}

func matchesRepository(ref, repository string) bool {
	repository = strings.TrimSpace(repository)
	if repository == "" {
		return false
	}
	for _, candidate := range repositoryVariants(ref) {
		if candidate == repository {
			return true
		}
	}
	return false
}

func resolveCurated(ref string) (Metadata, bool) {
	for _, candidate := range curatedRules {
		if matchesRepository(ref, candidate.repository) {
			return Metadata{
				Ref:          strings.TrimSpace(ref),
				Profile:      candidate.profile,
				Capabilities: append([]string(nil), candidate.capabilities...),
				Dangerous:    candidate.dangerous,
			}, true
		}
	}
	return Metadata{}, false
}

func missingMetadataError(ref string) error {
	return fmt.Errorf("%w: docker image %q is missing curated profile metadata; use a mapped image or add %s/%s/%s labels", ErrMetadataUnavailable, ref, LabelProfile, LabelCapabilities, LabelDangerous)
}

func DockerLabelProvider(binary string) LabelProvider {
	return func(ctx context.Context, ref string) (map[string]string, error) {
		if strings.TrimSpace(binary) == "" {
			return nil, nil
		}
		cmd := exec.CommandContext(ctx, binary, "image", "inspect", "--format", "{{json .Config.Labels}}", ref)
		out, err := cmd.Output()
		if err != nil {
			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) || errors.Is(err, exec.ErrNotFound) {
				return nil, nil
			}
			return nil, fmt.Errorf("inspect docker image %q labels: %w", ref, err)
		}
		trimmed := strings.TrimSpace(string(out))
		if trimmed == "" || trimmed == "null" {
			return nil, nil
		}
		var labels map[string]string
		if err := json.Unmarshal([]byte(trimmed), &labels); err != nil {
			return nil, fmt.Errorf("decode docker image %q labels: %w", ref, err)
		}
		return labels, nil
	}
}

func repositoryVariants(ref string) []string {
	repository := trimImageReference(ref)
	if repository == "" {
		return nil
	}
	variants := []string{repository}
	for _, prefix := range []string{"docker.io/library/", "index.docker.io/library/", "library/", "docker.io/"} {
		if stripped, ok := strings.CutPrefix(repository, prefix); ok {
			variants = append(variants, stripped)
		}
	}
	return uniqueStrings(variants)
}

func trimImageReference(ref string) string {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return ""
	}
	if idx := strings.Index(ref, "@"); idx >= 0 {
		ref = ref[:idx]
	}
	lastSlash := strings.LastIndex(ref, "/")
	lastColon := strings.LastIndex(ref, ":")
	if lastColon > lastSlash {
		ref = ref[:lastColon]
	}
	return ref
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}
