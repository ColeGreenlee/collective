package federation

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"strings"
	"sync"
	"time"
)

// InviteCode represents a pre-authenticated invitation to join the collective
type InviteCode struct {
	Code        string            // Random 16-char code
	Inviter     *FederatedAddress // alice@home.collective
	Permissions []DataStoreGrant  // What they'll have access to
	ExpiresAt   time.Time
	MaxUses     int
	Used        int
	CreatedAt   time.Time
	UsedBy      []*FederatedAddress // Track who used the invite

	// Federation configuration for automatic client setup
	CoordinatorAddress string // e.g., "localhost:8001"
	FederationDomain   string // e.g., "homelab.collective"
	FederationRootCA   []byte // PEM-encoded federation root CA
	Description        string // Optional description
}

// DataStoreGrant defines permissions granted through an invite
type DataStoreGrant struct {
	Path   string
	Rights []Right
}

// InviteManager manages invitation codes for the federation
type InviteManager struct {
	mu      sync.RWMutex
	invites map[string]*InviteCode

	// Cleanup settings
	cleanupInterval time.Duration
	stopCleanup     chan struct{}
}

// NewInviteManager creates a new invite manager
func NewInviteManager() *InviteManager {
	im := &InviteManager{
		invites:         make(map[string]*InviteCode),
		cleanupInterval: 1 * time.Hour,
		stopCleanup:     make(chan struct{}),
	}

	// Start cleanup goroutine
	go im.cleanupExpired()

	return im
}

// GenerateInvite creates a new invitation code
func (im *InviteManager) GenerateInvite(inviter *FederatedAddress, grants []DataStoreGrant, validity time.Duration, maxUses int) (*InviteCode, error) {
	im.mu.Lock()
	defer im.mu.Unlock()

	// Generate random code
	code, err := generateInviteCode()
	if err != nil {
		return nil, fmt.Errorf("failed to generate invite code: %w", err)
	}

	// Create invite
	invite := &InviteCode{
		Code:        code,
		Inviter:     inviter,
		Permissions: grants,
		ExpiresAt:   time.Now().Add(validity),
		MaxUses:     maxUses,
		Used:        0,
		CreatedAt:   time.Now(),
		UsedBy:      make([]*FederatedAddress, 0),
	}

	im.invites[code] = invite

	return invite, nil
}

// RedeemInvite validates and uses an invitation code
func (im *InviteManager) RedeemInvite(code string, newMember *FederatedAddress) (*InviteCode, error) {
	im.mu.Lock()
	defer im.mu.Unlock()

	invite, exists := im.invites[code]
	if !exists {
		return nil, fmt.Errorf("invalid invite code")
	}

	// Check expiration
	if time.Now().After(invite.ExpiresAt) {
		delete(im.invites, code)
		return nil, fmt.Errorf("invite code has expired")
	}

	// Check usage limit
	if invite.MaxUses > 0 && invite.Used >= invite.MaxUses {
		return nil, fmt.Errorf("invite code has reached usage limit")
	}

	// Check if member already used this invite
	for _, member := range invite.UsedBy {
		if member.Equal(newMember) {
			return nil, fmt.Errorf("invite already used by this member")
		}
	}

	// Mark as used
	invite.Used++
	invite.UsedBy = append(invite.UsedBy, newMember)

	// Remove if fully used
	if invite.MaxUses > 0 && invite.Used >= invite.MaxUses {
		delete(im.invites, code)
	}

	// Return a copy to prevent external modification
	inviteCopy := *invite
	return &inviteCopy, nil
}

// ValidateInvite checks if an invite code is valid without using it
func (im *InviteManager) ValidateInvite(code string) (*InviteCode, error) {
	im.mu.RLock()
	defer im.mu.RUnlock()

	invite, exists := im.invites[code]
	if !exists {
		return nil, fmt.Errorf("invalid invite code")
	}

	// Check expiration
	if time.Now().After(invite.ExpiresAt) {
		return nil, fmt.Errorf("invite code has expired")
	}

	// Check usage limit
	if invite.MaxUses > 0 && invite.Used >= invite.MaxUses {
		return nil, fmt.Errorf("invite code has reached usage limit")
	}

	// Return a copy
	inviteCopy := *invite
	return &inviteCopy, nil
}

// RevokeInvite cancels an invitation code
func (im *InviteManager) RevokeInvite(code string, revoker *FederatedAddress) error {
	im.mu.Lock()
	defer im.mu.Unlock()

	invite, exists := im.invites[code]
	if !exists {
		return fmt.Errorf("invite code not found")
	}

	// Only inviter can revoke
	if !invite.Inviter.Equal(revoker) {
		return fmt.Errorf("only the inviter can revoke this invite")
	}

	delete(im.invites, code)
	return nil
}

// ListInvites returns all active invites for an inviter
func (im *InviteManager) ListInvites(inviter *FederatedAddress) []*InviteCode {
	im.mu.RLock()
	defer im.mu.RUnlock()

	invites := make([]*InviteCode, 0)
	for _, invite := range im.invites {
		if invite.Inviter.Equal(inviter) {
			// Return a copy
			inviteCopy := *invite
			invites = append(invites, &inviteCopy)
		}
	}

	return invites
}

// GetInviteStats returns statistics about invite usage
func (im *InviteManager) GetInviteStats() map[string]interface{} {
	im.mu.RLock()
	defer im.mu.RUnlock()

	totalInvites := len(im.invites)
	totalUsed := 0
	expiringCount := 0
	now := time.Now()

	for _, invite := range im.invites {
		totalUsed += invite.Used
		if invite.ExpiresAt.Sub(now) < 24*time.Hour {
			expiringCount++
		}
	}

	return map[string]interface{}{
		"total_active":   totalInvites,
		"total_redeemed": totalUsed,
		"expiring_soon":  expiringCount,
	}
}

// cleanupExpired removes expired invitations
func (im *InviteManager) cleanupExpired() {
	ticker := time.NewTicker(im.cleanupInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			im.mu.Lock()
			now := time.Now()
			for code, invite := range im.invites {
				if now.After(invite.ExpiresAt) {
					delete(im.invites, code)
				}
			}
			im.mu.Unlock()

		case <-im.stopCleanup:
			return
		}
	}
}

// Stop stops the cleanup goroutine
func (im *InviteManager) Stop() {
	close(im.stopCleanup)
}

// generateInviteCode generates a secure random invite code
func generateInviteCode() (string, error) {
	// Generate 12 random bytes (will encode to 16 base64 chars)
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}

	// Encode to base64 and make URL-safe
	code := base64.URLEncoding.EncodeToString(b)

	// Remove padding if any
	code = code[:16]

	return code, nil
}

// InviteRequest represents a request to generate an invite
type InviteRequest struct {
	Inviter     *FederatedAddress
	Grants      []DataStoreGrant
	Validity    time.Duration
	MaxUses     int
	Description string // Optional description for the invite
}

// InviteResponse represents the response to an invite generation
type InviteResponse struct {
	Code      string
	ExpiresAt time.Time
	ShareURL  string // URL to share with invitee
}

// FormatShareURL creates a shareable URL for an invite
func (invite *InviteCode) FormatShareURL(coordinatorEndpoint string) string {
	// Format: collective://join/{coordinator}/{code}
	return fmt.Sprintf("collective://join/%s/%s", coordinatorEndpoint, invite.Code)
}

// ParseShareURL parses an invite URL
func ParseShareURL(url string) (coordinator string, code string, error error) {
	// Expected format: collective://join/{coordinator}/{code}
	prefix := "collective://join/"
	if !strings.HasPrefix(url, prefix) {
		return "", "", fmt.Errorf("invalid invite URL format")
	}

	parts := strings.Split(strings.TrimPrefix(url, prefix), "/")
	if len(parts) != 2 {
		return "", "", fmt.Errorf("invalid invite URL format")
	}

	return parts[0], parts[1], nil
}
