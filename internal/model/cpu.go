package model

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
)

const milliCPUPerCore = 1000

type CPUQuantity int64

func CPUCores(value int) CPUQuantity {
	return CPUQuantity(int64(value) * milliCPUPerCore)
}

func MilliCPU(value int64) CPUQuantity {
	return CPUQuantity(value)
}

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

func MustParseCPUQuantity(value string) CPUQuantity {
	parsed, err := ParseCPUQuantity(value)
	if err != nil {
		panic(err)
	}
	return parsed
}

func (q CPUQuantity) MarshalJSON() ([]byte, error) {
	if q%milliCPUPerCore == 0 {
		return []byte(strconv.FormatInt(int64(q/milliCPUPerCore), 10)), nil
	}
	value := strconv.FormatFloat(float64(q)/milliCPUPerCore, 'f', -1, 64)
	return []byte(value), nil
}

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

func (q CPUQuantity) MilliValue() int64 {
	return int64(q)
}

func (q CPUQuantity) VCPUCount() int {
	if q <= 0 {
		return 1
	}
	return int(math.Ceil(float64(q) / milliCPUPerCore))
}
