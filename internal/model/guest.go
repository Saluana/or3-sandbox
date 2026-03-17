package model

import (
	"sort"
	"strings"
)

// DefaultGuestControlProtocolVersion is the default guest control protocol
// version emitted in sandbox metadata.
const DefaultGuestControlProtocolVersion = "3"

// DefaultWorkspaceContractVersion is the default workspace contract version
// expected by the daemon and guest tooling.
const DefaultWorkspaceContractVersion = "1"

// DefaultImageContractVersion is the default guest image sidecar schema
// version.
const DefaultImageContractVersion = "1"

// NormalizeFeatures normalizes, deduplicates, and sorts feature-like string
// lists.
func NormalizeFeatures(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
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

// NormalizeCapabilities normalizes capability names using the same rules as
// [NormalizeFeatures].
func NormalizeCapabilities(values []string) []string {
	return NormalizeFeatures(values)
}
