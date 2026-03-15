package model

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
)

const milliCPUPerCore = 1000

// CPUQuantity stores CPU capacity in milli-CPU units.
type CPUQuantity int64

// CPUCores converts a whole-core count into a [CPUQuantity].
func CPUCores(value int) CPUQuantity {
	return CPUQuantity(int64(value) * milliCPUPerCore)
}

// MilliCPU converts a raw milli-CPU value into a [CPUQuantity].
func MilliCPU(value int64) CPUQuantity {
	return CPUQuantity(value)
}

// ParseCPUQuantity parses values like "2", "0.5", or "500m".
func ParseCPUQuantity(value string) (CPUQuantity, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return 0, fmt.Errorf("cpu value is required")
	}
	if strings.HasSuffix(trimmed, "m") {
		millis, err := strconv.ParseInt(strings.TrimSuffix(trimmed, "m"), 10, 64)
		if err != nil {
			return 0, fmt.Errorf("parse cpu milli value %q: %w", value, err)
		}
		return CPUQuantity(millis), nil
	}
	parts := strings.SplitN(trimmed, ".", 2)
	whole, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse cpu cores %q: %w", value, err)
	}
	millis := whole * milliCPUPerCore
	if len(parts) == 1 {
		return CPUQuantity(millis), nil
	}
	fractional := parts[1]
	if fractional == "" {
		return 0, fmt.Errorf("parse cpu cores %q: missing fractional digits", value)
	}
	if len(fractional) > 3 {
		return 0, fmt.Errorf("parse cpu cores %q: supports at most 3 decimal places", value)
	}
	for len(fractional) < 3 {
		fractional += "0"
	}
	fractionalMillis, err := strconv.ParseInt(fractional, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse cpu cores %q: %w", value, err)
	}
	if strings.HasPrefix(trimmed, "-") {
		fractionalMillis = -fractionalMillis
	}
	return CPUQuantity(millis + fractionalMillis), nil
}

// MustParseCPUQuantity parses value and panics on error.
func MustParseCPUQuantity(value string) CPUQuantity {
	parsed, err := ParseCPUQuantity(value)
	if err != nil {
		panic(err)
	}
	return parsed
}

// MarshalJSON encodes q as a JSON number using core units when possible.
func (q CPUQuantity) MarshalJSON() ([]byte, error) {
	if q%milliCPUPerCore == 0 {
		return []byte(strconv.FormatInt(int64(q/milliCPUPerCore), 10)), nil
	}
	value := strconv.FormatFloat(float64(q)/milliCPUPerCore, 'f', -1, 64)
	return []byte(value), nil
}

// UnmarshalJSON decodes q from a JSON number or string.
func (q *CPUQuantity) UnmarshalJSON(data []byte) error {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" || trimmed == "null" {
		*q = 0
		return nil
	}
	if trimmed[0] == '"' {
		var raw string
		if err := json.Unmarshal(data, &raw); err != nil {
			return err
		}
		parsed, err := ParseCPUQuantity(raw)
		if err != nil {
			return err
		}
		*q = parsed
		return nil
	}
	parsed, err := ParseCPUQuantity(trimmed)
	if err != nil {
		return err
	}
	*q = parsed
	return nil
}

// String formats q as a core value using up to three decimal places.
func (q CPUQuantity) String() string {
	if q%milliCPUPerCore == 0 {
		return strconv.FormatInt(int64(q/milliCPUPerCore), 10)
	}
	sign := ""
	value := int64(q)
	if value < 0 {
		sign = "-"
		value = -value
	}
	whole := value / milliCPUPerCore
	fractional := value % milliCPUPerCore
	decimal := fmt.Sprintf("%03d", fractional)
	decimal = strings.TrimRight(decimal, "0")
	return fmt.Sprintf("%s%d.%s", sign, whole, decimal)
}

// MilliValue returns q in milli-CPU units.
func (q CPUQuantity) MilliValue() int64 {
	return int64(q)
}

// VCPUCount returns the whole-vCPU ceiling used by runtimes that only accept
// integer CPU counts.
func (q CPUQuantity) VCPUCount() int {
	if q <= 0 {
		return 1
	}
	return int(math.Ceil(float64(q) / milliCPUPerCore))
}
