package federation

import (
	"strings"
	"testing"
	"time"
)

func TestInviteManager_GenerateInvite(t *testing.T) {
	im := NewInviteManager()
	defer im.Stop()
	
	inviter, _ := ParseAddress("alice@home.collective.local")
	grants := []DataStoreGrant{
		{
			Path:   "/shared",
			Rights: []Right{RightRead, RightWrite},
		},
	}
	
	invite, err := im.GenerateInvite(inviter, grants, 24*time.Hour, 5)
	if err != nil {
		t.Fatalf("Failed to generate invite: %v", err)
	}
	
	// Check invite properties
	if invite.Code == "" {
		t.Error("Invite code should not be empty")
	}
	
	if len(invite.Code) != 16 {
		t.Errorf("Expected 16-character code, got %d", len(invite.Code))
	}
	
	if !invite.Inviter.Equal(inviter) {
		t.Error("Inviter doesn't match")
	}
	
	if invite.MaxUses != 5 {
		t.Errorf("Expected MaxUses=5, got %d", invite.MaxUses)
	}
	
	if invite.Used != 0 {
		t.Error("New invite should have Used=0")
	}
	
	if len(invite.Permissions) != 1 {
		t.Errorf("Expected 1 permission grant, got %d", len(invite.Permissions))
	}
}

func TestInviteManager_RedeemInvite(t *testing.T) {
	im := NewInviteManager()
	defer im.Stop()
	
	inviter, _ := ParseAddress("alice@home.collective.local")
	bob, _ := ParseAddress("bob@garage.collective.local")
	
	grants := []DataStoreGrant{
		{Path: "/shared", Rights: []Right{RightRead}},
	}
	
	// Generate invite
	invite, _ := im.GenerateInvite(inviter, grants, 24*time.Hour, 2)
	
	// Redeem invite
	redeemed, err := im.RedeemInvite(invite.Code, bob)
	if err != nil {
		t.Fatalf("Failed to redeem invite: %v", err)
	}
	
	if redeemed.Used != 1 {
		t.Errorf("Expected Used=1, got %d", redeemed.Used)
	}
	
	if len(redeemed.UsedBy) != 1 || !redeemed.UsedBy[0].Equal(bob) {
		t.Error("UsedBy should contain bob")
	}
	
	// Try to redeem again with same member
	_, err = im.RedeemInvite(invite.Code, bob)
	if err == nil {
		t.Error("Should not allow same member to redeem twice")
	}
	
	// Redeem with different member
	carol, _ := ParseAddress("carol@house.collective.local")
	redeemed2, err := im.RedeemInvite(invite.Code, carol)
	if err != nil {
		t.Errorf("Should allow second redemption: %v", err)
	}
	
	if redeemed2.Used != 2 {
		t.Errorf("Expected Used=2, got %d", redeemed2.Used)
	}
	
	// Third redemption should fail (max uses = 2)
	dave, _ := ParseAddress("dave@apartment.collective.local")
	_, err = im.RedeemInvite(invite.Code, dave)
	if err == nil {
		t.Error("Should not allow redemption beyond max uses")
	}
}

func TestInviteManager_ExpiredInvite(t *testing.T) {
	im := NewInviteManager()
	defer im.Stop()
	
	inviter, _ := ParseAddress("alice@home.collective.local")
	bob, _ := ParseAddress("bob@garage.collective.local")
	
	// Generate invite with very short validity
	invite, _ := im.GenerateInvite(inviter, nil, 100*time.Millisecond, 1)
	
	// Wait for expiry
	time.Sleep(150 * time.Millisecond)
	
	// Try to redeem expired invite
	_, err := im.RedeemInvite(invite.Code, bob)
	if err == nil {
		t.Error("Should not allow redemption of expired invite")
	}
	
	if !strings.Contains(err.Error(), "expired") {
		t.Errorf("Expected expiry error, got: %v", err)
	}
}

func TestInviteManager_ValidateInvite(t *testing.T) {
	im := NewInviteManager()
	defer im.Stop()
	
	inviter, _ := ParseAddress("alice@home.collective.local")
	invite, _ := im.GenerateInvite(inviter, nil, 24*time.Hour, 1)
	
	// Validate without using
	validated, err := im.ValidateInvite(invite.Code)
	if err != nil {
		t.Fatalf("Failed to validate invite: %v", err)
	}
	
	if validated.Used != 0 {
		t.Error("Validation should not increment usage")
	}
	
	// Validate invalid code
	_, err = im.ValidateInvite("invalid-code")
	if err == nil {
		t.Error("Should fail to validate invalid code")
	}
}

func TestInviteManager_RevokeInvite(t *testing.T) {
	im := NewInviteManager()
	defer im.Stop()
	
	alice, _ := ParseAddress("alice@home.collective.local")
	bob, _ := ParseAddress("bob@garage.collective.local")
	
	// Generate invite
	invite, _ := im.GenerateInvite(alice, nil, 24*time.Hour, 5)
	
	// Try to revoke as non-inviter
	err := im.RevokeInvite(invite.Code, bob)
	if err == nil {
		t.Error("Non-inviter should not be able to revoke")
	}
	
	// Revoke as inviter
	err = im.RevokeInvite(invite.Code, alice)
	if err != nil {
		t.Fatalf("Inviter should be able to revoke: %v", err)
	}
	
	// Try to use revoked invite
	_, err = im.RedeemInvite(invite.Code, bob)
	if err == nil {
		t.Error("Should not be able to redeem revoked invite")
	}
}

func TestInviteManager_ListInvites(t *testing.T) {
	im := NewInviteManager()
	defer im.Stop()
	
	alice, _ := ParseAddress("alice@home.collective.local")
	bob, _ := ParseAddress("bob@garage.collective.local")
	
	// Generate invites for alice
	im.GenerateInvite(alice, nil, 24*time.Hour, 1)
	im.GenerateInvite(alice, nil, 24*time.Hour, 2)
	
	// Generate invite for bob
	im.GenerateInvite(bob, nil, 24*time.Hour, 1)
	
	// List alice's invites
	aliceInvites := im.ListInvites(alice)
	if len(aliceInvites) != 2 {
		t.Errorf("Expected 2 invites for alice, got %d", len(aliceInvites))
	}
	
	// List bob's invites
	bobInvites := im.ListInvites(bob)
	if len(bobInvites) != 1 {
		t.Errorf("Expected 1 invite for bob, got %d", len(bobInvites))
	}
}

func TestInviteManager_GetInviteStats(t *testing.T) {
	im := NewInviteManager()
	defer im.Stop()
	
	alice, _ := ParseAddress("alice@home.collective.local")
	bob, _ := ParseAddress("bob@garage.collective.local")
	
	// Generate some invites
	invite1, _ := im.GenerateInvite(alice, nil, 48*time.Hour, 2)  // Not expiring
	im.GenerateInvite(alice, nil, 72*time.Hour, 5)  // Not expiring
	im.GenerateInvite(alice, nil, 30*time.Minute, 1) // Expiring soon
	
	// Use one invite
	im.RedeemInvite(invite1.Code, bob)
	
	stats := im.GetInviteStats()
	
	if stats["total_active"].(int) != 3 {
		t.Errorf("Expected 3 active invites, got %d", stats["total_active"])
	}
	
	if stats["total_redeemed"].(int) != 1 {
		t.Errorf("Expected 1 redeemed invite, got %d", stats["total_redeemed"])
	}
	
	if stats["expiring_soon"].(int) != 1 {
		t.Errorf("Expected 1 expiring invite, got %d", stats["expiring_soon"])
	}
}

func TestInviteShareURL(t *testing.T) {
	inviter, _ := ParseAddress("alice@home.collective.local")
	invite := &InviteCode{
		Code:    "abc123XYZ456test",
		Inviter: inviter,
	}
	
	// Test URL formatting
	url := invite.FormatShareURL("coordinator.example.com:8001")
	expected := "collective://join/coordinator.example.com:8001/abc123XYZ456test"
	
	if url != expected {
		t.Errorf("Expected URL %s, got %s", expected, url)
	}
	
	// Test URL parsing
	coordinator, code, err := ParseShareURL(url)
	if err != nil {
		t.Fatalf("Failed to parse URL: %v", err)
	}
	
	if coordinator != "coordinator.example.com:8001" {
		t.Errorf("Expected coordinator 'coordinator.example.com:8001', got '%s'", coordinator)
	}
	
	if code != "abc123XYZ456test" {
		t.Errorf("Expected code 'abc123XYZ456test', got '%s'", code)
	}
	
	// Test invalid URL
	_, _, err = ParseShareURL("https://invalid.url")
	if err == nil {
		t.Error("Should fail to parse invalid URL")
	}
}

func TestInviteCodeGeneration(t *testing.T) {
	// Generate multiple codes and check uniqueness
	codes := make(map[string]bool)
	
	for i := 0; i < 100; i++ {
		code, err := generateInviteCode()
		if err != nil {
			t.Fatalf("Failed to generate code: %v", err)
		}
		
		if len(code) != 16 {
			t.Errorf("Expected 16-character code, got %d", len(code))
		}
		
		// Check for duplicates
		if codes[code] {
			t.Errorf("Duplicate code generated: %s", code)
		}
		codes[code] = true
		
		// Verify it's URL-safe
		if strings.Contains(code, "+") || strings.Contains(code, "/") {
			t.Error("Code should be URL-safe")
		}
	}
}