package model

import (
	"encoding/json"
	"testing"
)

func TestParseCPUQuantity(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name  string
		input string
		want  CPUQuantity
	}{
		{name: "integer cores", input: "2", want: 2000},
		{name: "decimal cores", input: "1.5", want: 1500},
		{name: "millicores", input: "250m", want: 250},
		{name: "negative decimal cores", input: "-0.5", want: -500},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := ParseCPUQuantity(tc.input)
			if err != nil {
				t.Fatalf("ParseCPUQuantity(%q): %v", tc.input, err)
			}
			if got != tc.want {
				t.Fatalf("ParseCPUQuantity(%q) = %d, want %d", tc.input, got, tc.want)
			}
		})
	}
}

func TestCPUCores(t *testing.T) {
	t.Parallel()

	if got := CPUCores(2); got != 2000 {
		t.Fatalf("CPUCores(2) = %d, want 2000", got)
	}
}

func TestParseCPUQuantityRejectsPrecisionBeyondMillicores(t *testing.T) {
	t.Parallel()

	if _, err := ParseCPUQuantity("0.0001"); err == nil {
		t.Fatal("expected precision error")
	}
}

func TestCPUQuantityJSONRoundTrip(t *testing.T) {
	t.Parallel()

	var decimal CPUQuantity
	if err := json.Unmarshal([]byte(`1.5`), &decimal); err != nil {
		t.Fatalf("unmarshal decimal: %v", err)
	}
	if decimal != 1500 {
		t.Fatalf("unexpected decimal quantity %d", decimal)
	}
	data, err := json.Marshal(decimal)
	if err != nil {
		t.Fatalf("marshal decimal: %v", err)
	}
	if string(data) != "1.5" {
		t.Fatalf("unexpected decimal JSON %s", data)
	}

	var milli CPUQuantity
	if err := json.Unmarshal([]byte(`"250m"`), &milli); err != nil {
		t.Fatalf("unmarshal millicores: %v", err)
	}
	if milli != 250 {
		t.Fatalf("unexpected millicore quantity %d", milli)
	}
}

func TestCPUQuantityStringAndVCPUCount(t *testing.T) {
	t.Parallel()

	if got := MustParseCPUQuantity("1500m").String(); got != "1.5" {
		t.Fatalf("unexpected string form %q", got)
	}
	if got := MustParseCPUQuantity("1500m").VCPUCount(); got != 2 {
		t.Fatalf("unexpected vcpu count %d", got)
	}
	if got := MustParseCPUQuantity("250m").VCPUCount(); got != 1 {
		t.Fatalf("unexpected vcpu count for sub-core quantity %d", got)
	}
}
