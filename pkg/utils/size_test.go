package utils

import (
	"testing"
)

func TestParseDataSize(t *testing.T) {
	tests := []struct {
		input    string
		expected int64
		wantErr  bool
	}{
		// Just numbers (bytes)
		{"0", 0, false},
		{"1024", 1024, false},
		{"1073741824", 1073741824, false},
		
		// Bytes with unit
		{"0B", 0, false},
		{"100B", 100, false},
		{"1024B", 1024, false},
		
		// Kilobytes - decimal (1000-based)
		{"1KB", 1000, false},
		{"1.5KB", 1500, false},
		{"10KB", 10000, false},
		
		// Kilobytes - binary (1024-based)
		{"1K", 1024, false},
		{"1KiB", 1024, false},
		{"1.5KiB", 1536, false},
		{"10KiB", 10240, false},
		
		// Megabytes - decimal
		{"1MB", 1000000, false},
		{"1.5MB", 1500000, false},
		{"100MB", 100000000, false},
		
		// Megabytes - binary
		{"1M", 1048576, false},
		{"1MiB", 1048576, false},
		{"1.5MiB", 1572864, false},
		{"100MiB", 104857600, false},
		
		// Gigabytes - decimal
		{"1GB", 1000000000, false},
		{"1.5GB", 1500000000, false},
		{"40GB", 40000000000, false},
		
		// Gigabytes - binary
		{"1G", 1073741824, false},
		{"1GiB", 1073741824, false},
		{"1.5GiB", 1610612736, false},
		{"40GiB", 42949672960, false},
		
		// Terabytes - decimal
		{"1TB", 1000000000000, false},
		{"1.485TB", 1485000000000, false},
		
		// Terabytes - binary
		{"1T", 1099511627776, false},
		{"1TiB", 1099511627776, false},
		{"1.5TiB", 1649267441664, false},
		
		// Petabytes - decimal
		{"1PB", 1000000000000000, false},
		
		// Petabytes - binary
		{"1P", 1125899906842624, false},
		{"1PiB", 1125899906842624, false},
		
		// Case insensitive
		{"1gb", 1000000000, false},
		{"1Gb", 1000000000, false},
		{"1GB", 1000000000, false},
		{"1gib", 1073741824, false},
		{"1Gib", 1073741824, false},
		
		// With spaces
		{"1 GB", 1000000000, false},
		{"1.5 TB", 1500000000000, false},
		{" 100 MB ", 100000000, false},
		
		// Error cases
		{"", 0, true},
		{"invalid", 0, true},
		{"GB", 0, true},
		{"1.2.3GB", 0, true},
		{"1XB", 0, true},
		{"-1GB", 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := ParseDataSize(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseDataSize(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
				return
			}
			if got != tt.expected {
				t.Errorf("ParseDataSize(%q) = %v, want %v", tt.input, got, tt.expected)
			}
		})
	}
}

func TestFormatDataSize(t *testing.T) {
	tests := []struct {
		input    int64
		expected string
	}{
		{0, "0 B"},
		{1, "1 B"},
		{1023, "1023 B"},
		{1024, "1 KB"},
		{1536, "1.5 KB"},
		{10240, "10 KB"},
		{1048576, "1 MB"},
		{1572864, "1.5 MB"},
		{104857600, "100 MB"},
		{1073741824, "1 GB"},
		{1610612736, "1.5 GB"},
		{42949672960, "40 GB"},
		{1099511627776, "1 TB"},
		{1649267441664, "1.5 TB"},
		{1125899906842624, "1 PB"},
		{-1, "invalid"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			got := FormatDataSize(tt.input)
			if got != tt.expected {
				t.Errorf("FormatDataSize(%v) = %v, want %v", tt.input, got, tt.expected)
			}
		})
	}
}

func TestParseDataSizeWithDefault(t *testing.T) {
	defaultSize := int64(1073741824) // 1GB

	tests := []struct {
		input    string
		expected int64
	}{
		{"", defaultSize},
		{"invalid", defaultSize},
		{"1GB", 1000000000},
		{"2GiB", 2147483648},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := ParseDataSizeWithDefault(tt.input, defaultSize)
			if got != tt.expected {
				t.Errorf("ParseDataSizeWithDefault(%q, %v) = %v, want %v", 
					tt.input, defaultSize, got, tt.expected)
			}
		})
	}
}