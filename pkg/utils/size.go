package utils

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// ParseDataSize parses human-friendly data sizes like "1GB", "1.5TB", "512MB", "100KB"
// and returns the size in bytes. It supports:
// - B (bytes)
// - KB/K (kilobytes)
// - MB/M (megabytes)
// - GB/G (gigabytes)
// - TB/T (terabytes)
// - PB/P (petabytes)
// Both binary (1024-based) and decimal (1000-based) units are supported:
// - KiB, MiB, GiB, TiB, PiB for binary units
func ParseDataSize(sizeStr string) (int64, error) {
	sizeStr = strings.TrimSpace(sizeStr)
	if sizeStr == "" {
		return 0, fmt.Errorf("empty size string")
	}

	// Check if it's just a number (bytes)
	if val, err := strconv.ParseInt(sizeStr, 10, 64); err == nil {
		return val, nil
	}

	// Regular expression to match size format: number (with optional decimal) followed by unit
	re := regexp.MustCompile(`^([\d.]+)\s*([A-Za-z]+)$`)
	matches := re.FindStringSubmatch(sizeStr)
	if len(matches) != 3 {
		return 0, fmt.Errorf("invalid size format: %s (expected format like '1GB', '512MB', '1.5TB')", sizeStr)
	}

	// Parse the numeric part
	value, err := strconv.ParseFloat(matches[1], 64)
	if err != nil {
		return 0, fmt.Errorf("invalid numeric value: %s", matches[1])
	}

	// Parse the unit and get multiplier
	unit := strings.ToUpper(matches[2])
	multiplier := getMultiplier(unit)
	if multiplier == 0 {
		return 0, fmt.Errorf("unknown unit: %s (supported: B, KB, MB, GB, TB, PB, KiB, MiB, GiB, TiB, PiB)", matches[2])
	}

	// Calculate size in bytes
	bytes := int64(value * float64(multiplier))

	// Validate result
	if bytes < 0 {
		return 0, fmt.Errorf("size overflow or negative value")
	}

	return bytes, nil
}

// FormatDataSize formats bytes into human-readable format
func FormatDataSize(bytes int64) string {
	if bytes < 0 {
		return "invalid"
	}

	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}

	units := []string{"B", "KB", "MB", "GB", "TB", "PB"}
	exp := 0
	div := int64(unit)

	for n := bytes / unit; n >= unit && exp < len(units)-2; n /= unit {
		div *= unit
		exp++
	}
	exp++ // Adjust for the initial division

	value := float64(bytes) / float64(div)

	// Format with appropriate decimal places
	if value == float64(int64(value)) {
		return fmt.Sprintf("%.0f %s", value, units[exp])
	} else if value*10 == float64(int64(value*10)) {
		return fmt.Sprintf("%.1f %s", value, units[exp])
	}
	return fmt.Sprintf("%.2f %s", value, units[exp])
}

// getMultiplier returns the byte multiplier for a given unit
func getMultiplier(unit string) int64 {
	switch unit {
	// Bytes
	case "B", "BYTE", "BYTES":
		return 1

	// Decimal units (1000-based)
	case "KB":
		return 1000
	case "MB":
		return 1000 * 1000
	case "GB":
		return 1000 * 1000 * 1000
	case "TB":
		return 1000 * 1000 * 1000 * 1000
	case "PB":
		return 1000 * 1000 * 1000 * 1000 * 1000

	// Binary units (1024-based) - Standard IEC units
	case "KIB", "K":
		return 1024
	case "MIB", "M":
		return 1024 * 1024
	case "GIB", "G":
		return 1024 * 1024 * 1024
	case "TIB", "T":
		return 1024 * 1024 * 1024 * 1024
	case "PIB", "P":
		return 1024 * 1024 * 1024 * 1024 * 1024

	default:
		return 0
	}
}

// Common size constants for convenience
const (
	Byte     int64 = 1
	KiloByte int64 = 1024
	MegaByte int64 = 1024 * 1024
	GigaByte int64 = 1024 * 1024 * 1024
	TeraByte int64 = 1024 * 1024 * 1024 * 1024
	PetaByte int64 = 1024 * 1024 * 1024 * 1024 * 1024
)

// ParseDataSizeWithDefault parses a size string and returns default if empty or invalid
func ParseDataSizeWithDefault(sizeStr string, defaultSize int64) int64 {
	if sizeStr == "" {
		return defaultSize
	}

	size, err := ParseDataSize(sizeStr)
	if err != nil {
		return defaultSize
	}

	return size
}
