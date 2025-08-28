package federation

import (
	"strings"
	"testing"
)

func TestParseAddress(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		want      *FederatedAddress
		wantError bool
		errorMsg  string
	}{
		// Valid node addresses
		{
			name:  "valid node address",
			input: "node-01@alice.collective.local",
			want: &FederatedAddress{
				LocalPart: "node-01",
				Domain:    "alice.collective.local",
			},
			wantError: false,
		},
		{
			name:  "valid coordinator address",
			input: "coord@bob.collective.local",
			want: &FederatedAddress{
				LocalPart: "coord",
				Domain:    "bob.collective.local",
			},
			wantError: false,
		},
		{
			name:  "valid member identity",
			input: "alice@home.collective",
			want: &FederatedAddress{
				LocalPart: "alice",
				Domain:    "home.collective",
			},
			wantError: false,
		},
		{
			name:  "valid resource address",
			input: "/media/movies@carol.collective.local",
			want: &FederatedAddress{
				Resource: "/media/movies",
				Domain:   "carol.collective.local",
			},
			wantError: false,
		},
		{
			name:  "valid nested resource",
			input: "/files/data.txt@alice.collective.local",
			want: &FederatedAddress{
				Resource: "/files/data.txt",
				Domain:   "alice.collective.local",
			},
			wantError: false,
		},
		{
			name:  "valid deep path resource",
			input: "/backups/2024/photos/family@storage.collective.local",
			want: &FederatedAddress{
				Resource: "/backups/2024/photos/family",
				Domain:   "storage.collective.local",
			},
			wantError: false,
		},
		{
			name:  "complex domain",
			input: "node-west-1@alice.us-west.collective.local",
			want: &FederatedAddress{
				LocalPart: "node-west-1",
				Domain:    "alice.us-west.collective.local",
			},
			wantError: false,
		},
		{
			name:  "numeric local part",
			input: "node123@test.collective.local",
			want: &FederatedAddress{
				LocalPart: "node123",
				Domain:    "test.collective.local",
			},
			wantError: false,
		},
		{
			name:  "underscore in local part",
			input: "node_backup@alice.collective.local",
			want: &FederatedAddress{
				LocalPart: "node_backup",
				Domain:    "alice.collective.local",
			},
			wantError: false,
		},
		{
			name:  "short domain",
			input: "coord@a.b",
			want: &FederatedAddress{
				LocalPart: "coord",
				Domain:    "a.b",
			},
			wantError: false,
		},

		// Invalid formats
		{
			name:      "empty address",
			input:     "",
			wantError: true,
			errorMsg:  "address cannot be empty",
		},
		{
			name:      "missing @ symbol",
			input:     "alice",
			wantError: true,
			errorMsg:  "must contain exactly one @ symbol",
		},
		{
			name:      "missing domain",
			input:     "alice@",
			wantError: true,
			errorMsg:  "domain cannot be empty",
		},
		{
			name:      "missing local part",
			input:     "@domain",
			wantError: true,
			errorMsg:  "domain must contain at least one dot",
		},
		{
			name:      "multiple @ symbols",
			input:     "alice@bob@collective.local",
			wantError: true,
			errorMsg:  "must contain exactly one @ symbol",
		},
		{
			name:      "domain without dot",
			input:     "alice@domain",
			wantError: true,
			errorMsg:  "domain must contain at least one dot",
		},
		{
			name:      "just slash resource",
			input:     "/@alice.collective.local",
			wantError: true,
			errorMsg:  "resource path cannot be just /",
		},
		{
			name:      "spaces in address",
			input:     "alice smith@collective.local",
			wantError: false, // Spaces are allowed in local part
			want: &FederatedAddress{
				LocalPart: "alice smith",
				Domain:    "collective.local",
			},
		},
		{
			name:      "special characters in local part",
			input:     "node-01.backup@alice.collective.local",
			wantError: false,
			want: &FederatedAddress{
				LocalPart: "node-01.backup",
				Domain:    "alice.collective.local",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseAddress(tt.input)

			if tt.wantError {
				if err == nil {
					t.Errorf("ParseAddress(%q) expected error, got nil", tt.input)
				} else if tt.errorMsg != "" && !contains(err.Error(), tt.errorMsg) {
					t.Errorf("ParseAddress(%q) error = %v, want error containing %q", tt.input, err, tt.errorMsg)
				}
				return
			}

			if err != nil {
				t.Errorf("ParseAddress(%q) unexpected error: %v", tt.input, err)
				return
			}

			if !got.Equal(tt.want) {
				t.Errorf("ParseAddress(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestFederatedAddress_String(t *testing.T) {
	tests := []struct {
		name string
		addr *FederatedAddress
		want string
	}{
		{
			name: "node address",
			addr: &FederatedAddress{
				LocalPart: "node-01",
				Domain:    "alice.collective.local",
			},
			want: "node-01@alice.collective.local",
		},
		{
			name: "resource address",
			addr: &FederatedAddress{
				Resource: "/media/movies",
				Domain:   "carol.collective.local",
			},
			want: "/media/movies@carol.collective.local",
		},
		{
			name: "nil address",
			addr: nil,
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.addr.String()
			if got != tt.want {
				t.Errorf("String() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFederatedAddress_IsLocal(t *testing.T) {
	tests := []struct {
		name     string
		addr     *FederatedAddress
		myDomain string
		want     bool
	}{
		{
			name: "local domain match",
			addr: &FederatedAddress{
				LocalPart: "node-01",
				Domain:    "alice.collective.local",
			},
			myDomain: "alice.collective.local",
			want:     true,
		},
		{
			name: "different domain",
			addr: &FederatedAddress{
				LocalPart: "node-01",
				Domain:    "alice.collective.local",
			},
			myDomain: "bob.collective.local",
			want:     false,
		},
		{
			name:     "nil address",
			addr:     nil,
			myDomain: "alice.collective.local",
			want:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.addr.IsLocal(tt.myDomain)
			if got != tt.want {
				t.Errorf("IsLocal(%q) = %v, want %v", tt.myDomain, got, tt.want)
			}
		})
	}
}

func TestFederatedAddress_IsResource(t *testing.T) {
	tests := []struct {
		name string
		addr *FederatedAddress
		want bool
	}{
		{
			name: "resource address",
			addr: &FederatedAddress{
				Resource: "/media/movies",
				Domain:   "alice.collective.local",
			},
			want: true,
		},
		{
			name: "node address",
			addr: &FederatedAddress{
				LocalPart: "node-01",
				Domain:    "alice.collective.local",
			},
			want: false,
		},
		{
			name: "nil address",
			addr: nil,
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.addr.IsResource()
			if got != tt.want {
				t.Errorf("IsResource() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestFederatedAddress_Validate(t *testing.T) {
	tests := []struct {
		name      string
		addr      *FederatedAddress
		wantError bool
		errorMsg  string
	}{
		{
			name: "valid node address",
			addr: &FederatedAddress{
				LocalPart: "node-01",
				Domain:    "alice.collective.local",
			},
			wantError: false,
		},
		{
			name: "valid resource address",
			addr: &FederatedAddress{
				Resource: "/media/movies",
				Domain:   "alice.collective.local",
			},
			wantError: false,
		},
		{
			name:      "nil address",
			addr:      nil,
			wantError: true,
			errorMsg:  "address is nil",
		},
		{
			name: "empty domain",
			addr: &FederatedAddress{
				LocalPart: "node-01",
				Domain:    "",
			},
			wantError: true,
			errorMsg:  "domain cannot be empty",
		},
		{
			name: "domain without dot",
			addr: &FederatedAddress{
				LocalPart: "node-01",
				Domain:    "alice",
			},
			wantError: true,
			errorMsg:  "domain must contain at least one dot",
		},
		{
			name: "neither local part nor resource",
			addr: &FederatedAddress{
				Domain: "alice.collective.local",
			},
			wantError: true,
			errorMsg:  "address must have either LocalPart or Resource",
		},
		{
			name: "both local part and resource",
			addr: &FederatedAddress{
				LocalPart: "node-01",
				Resource:  "/media",
				Domain:    "alice.collective.local",
			},
			wantError: true,
			errorMsg:  "address cannot have both LocalPart and Resource",
		},
		{
			name: "resource without slash",
			addr: &FederatedAddress{
				Resource: "media",
				Domain:   "alice.collective.local",
			},
			wantError: true,
			errorMsg:  "resource must start with /",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.addr.Validate()

			if tt.wantError {
				if err == nil {
					t.Errorf("Validate() expected error, got nil")
				} else if tt.errorMsg != "" && !contains(err.Error(), tt.errorMsg) {
					t.Errorf("Validate() error = %v, want error containing %q", err, tt.errorMsg)
				}
			} else {
				if err != nil {
					t.Errorf("Validate() unexpected error: %v", err)
				}
			}
		})
	}
}

func TestFederatedAddress_Equal(t *testing.T) {
	tests := []struct {
		name string
		a    *FederatedAddress
		b    *FederatedAddress
		want bool
	}{
		{
			name: "equal node addresses",
			a: &FederatedAddress{
				LocalPart: "node-01",
				Domain:    "alice.collective.local",
			},
			b: &FederatedAddress{
				LocalPart: "node-01",
				Domain:    "alice.collective.local",
			},
			want: true,
		},
		{
			name: "equal resource addresses",
			a: &FederatedAddress{
				Resource: "/media/movies",
				Domain:   "alice.collective.local",
			},
			b: &FederatedAddress{
				Resource: "/media/movies",
				Domain:   "alice.collective.local",
			},
			want: true,
		},
		{
			name: "different local parts",
			a: &FederatedAddress{
				LocalPart: "node-01",
				Domain:    "alice.collective.local",
			},
			b: &FederatedAddress{
				LocalPart: "node-02",
				Domain:    "alice.collective.local",
			},
			want: false,
		},
		{
			name: "different domains",
			a: &FederatedAddress{
				LocalPart: "node-01",
				Domain:    "alice.collective.local",
			},
			b: &FederatedAddress{
				LocalPart: "node-01",
				Domain:    "bob.collective.local",
			},
			want: false,
		},
		{
			name: "nil addresses",
			a:    nil,
			b:    nil,
			want: true,
		},
		{
			name: "one nil address",
			a: &FederatedAddress{
				LocalPart: "node-01",
				Domain:    "alice.collective.local",
			},
			b:    nil,
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.a.Equal(tt.b)
			if got != tt.want {
				t.Errorf("Equal() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestFederatedAddress_GetIdentifier(t *testing.T) {
	tests := []struct {
		name string
		addr *FederatedAddress
		want string
	}{
		{
			name: "local part identifier",
			addr: &FederatedAddress{
				LocalPart: "node-01",
				Domain:    "alice.collective.local",
			},
			want: "node-01",
		},
		{
			name: "resource identifier",
			addr: &FederatedAddress{
				Resource: "/media/movies",
				Domain:   "alice.collective.local",
			},
			want: "/media/movies",
		},
		{
			name: "nil address",
			addr: nil,
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.addr.GetIdentifier()
			if got != tt.want {
				t.Errorf("GetIdentifier() = %q, want %q", got, tt.want)
			}
		})
	}
}

// Helper function to check if a string contains a substring
func contains(s, substr string) bool {
	return strings.Contains(s, substr)
}

// Additional test for string import usage
func TestStringImportUsage(t *testing.T) {
	// This test ensures the strings package import is used
	result := contains("hello world", "world")
	if !result {
		t.Error("contains function should return true for 'hello world' containing 'world'")
	}
}