package model

import (
	"sort"
	"strings"
)

const DefaultGuestControlProtocolVersion = "1"
const DefaultWorkspaceContractVersion = "1"
const DefaultImageContractVersion = "1"

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

func NormalizeCapabilities(values []string) []string {
	return NormalizeFeatures(values)
}
