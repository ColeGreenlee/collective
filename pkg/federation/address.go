package federation

import (
	"fmt"
	"strings"
)

// FederatedAddress represents a federation address in Mastodon-style format
// Examples:
//   - node-01@alice.collective.local (storage node)
//   - coord@bob.collective.local (coordinator)
//   - /media/movies@carol.collective.local (DataStore resource)
//   - alice@home.collective (member identity)
type FederatedAddress struct {
	LocalPart string // node-01, coord, alice
	Domain    string // alice.collective.local
	Resource  string // /media/movies (optional DataStore path)
}

// ParseAddress parses a federated address string into its components
// Supports formats:
//   - localpart@domain (e.g., node-01@alice.collective.local)
//   - /resource/path@domain (e.g., /media/movies@alice.collective.local)
func ParseAddress(addr string) (*FederatedAddress, error) {
	if addr == "" {
		return nil, fmt.Errorf("address cannot be empty")
	}

	// Check if address contains @
	parts := strings.Split(addr, "@")
	if len(parts) != 2 {
		return nil, fmt.Errorf("invalid address format: must contain exactly one @ symbol")
	}

	localOrResource := parts[0]
	domain := parts[1]

	// Validate domain (must contain at least one dot)
	if domain == "" {
		return nil, fmt.Errorf("domain cannot be empty")
	}
	if !strings.Contains(domain, ".") {
		return nil, fmt.Errorf("domain must contain at least one dot (e.g., alice.collective.local)")
	}

	fa := &FederatedAddress{
		Domain: domain,
	}

	// Check if it's a resource path (starts with /)
	if strings.HasPrefix(localOrResource, "/") {
		if len(localOrResource) == 1 {
			return nil, fmt.Errorf("resource path cannot be just /")
		}
		fa.Resource = localOrResource
	} else {
		// It's a local part (node, coordinator, or member identity)
		if localOrResource == "" {
			return nil, fmt.Errorf("local part cannot be empty")
		}
		fa.LocalPart = localOrResource
	}

	return fa, nil
}

// String returns the canonical string representation of the federated address
func (a *FederatedAddress) String() string {
	if a == nil {
		return ""
	}

	if a.Resource != "" {
		return fmt.Sprintf("%s@%s", a.Resource, a.Domain)
	}
	return fmt.Sprintf("%s@%s", a.LocalPart, a.Domain)
}

// IsLocal returns true if this address belongs to the specified domain
func (a *FederatedAddress) IsLocal(myDomain string) bool {
	if a == nil {
		return false
	}
	return a.Domain == myDomain
}

// IsResource returns true if this address represents a DataStore resource
func (a *FederatedAddress) IsResource() bool {
	if a == nil {
		return false
	}
	return a.Resource != ""
}

// GetIdentifier returns the non-domain part of the address (LocalPart or Resource)
func (a *FederatedAddress) GetIdentifier() string {
	if a == nil {
		return ""
	}
	if a.Resource != "" {
		return a.Resource
	}
	return a.LocalPart
}

// Validate checks if the federated address is valid
func (a *FederatedAddress) Validate() error {
	if a == nil {
		return fmt.Errorf("address is nil")
	}
	if a.Domain == "" {
		return fmt.Errorf("domain cannot be empty")
	}
	if !strings.Contains(a.Domain, ".") {
		return fmt.Errorf("domain must contain at least one dot")
	}
	if a.LocalPart == "" && a.Resource == "" {
		return fmt.Errorf("address must have either LocalPart or Resource")
	}
	if a.LocalPart != "" && a.Resource != "" {
		return fmt.Errorf("address cannot have both LocalPart and Resource")
	}
	if a.Resource != "" && !strings.HasPrefix(a.Resource, "/") {
		return fmt.Errorf("resource must start with /")
	}
	return nil
}

// Equal returns true if two federated addresses are equivalent
func (a *FederatedAddress) Equal(other *FederatedAddress) bool {
	if a == nil && other == nil {
		return true
	}
	if a == nil || other == nil {
		return false
	}
	return a.LocalPart == other.LocalPart &&
		a.Domain == other.Domain &&
		a.Resource == other.Resource
}