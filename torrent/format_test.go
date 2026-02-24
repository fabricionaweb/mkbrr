package torrent

import (
	"testing"
)

// TestParseFormat tests the ParseFormat function with various input formats
// including numeric values (1, 2, 3), named values (v1, v2, hybrid),
// case variations, whitespace handling, and invalid inputs.
func TestParseFormat(t *testing.T) {
	tests := []struct {
		input    string
		expected int
		wantErr  bool
	}{
		// Valid numeric inputs
		{"1", FormatV1, false},
		{"2", FormatV2, false},
		{"3", FormatHybrid, false},
		// Valid named inputs
		{"v1", FormatV1, false},
		{"V1", FormatV1, false},
		{"v2", FormatV2, false},
		{"V2", FormatV2, false},
		{"hybrid", FormatHybrid, false},
		{"HYBRID", FormatHybrid, false},
		{"Hybrid", FormatHybrid, false},
		// With whitespace
		{" 1 ", FormatV1, false},
		{" v2 ", FormatV2, false},
		// Invalid inputs
		{"", 0, true},
		{"0", 0, true},
		{"4", 0, true},
		{"v3", 0, true},
		{"invalid", 0, true},
		{"v1v2", 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := ParseFormat(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseFormat(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
				return
			}
			if got != tt.expected {
				t.Errorf("ParseFormat(%q) = %d, want %d", tt.input, got, tt.expected)
			}
		})
	}
}
