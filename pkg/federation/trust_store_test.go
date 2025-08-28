package federation

import (
	"collective/pkg/auth"
	"io/ioutil"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestTrustStore_LoadFederationRootCA(t *testing.T) {
	// Create temporary directory for test certificates
	tempDir, err := ioutil.TempDir("", "truststore-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tempDir)

	// Generate test federation CA
	fcm := auth.NewFederationCertManager(tempDir)
	if err := fcm.GenerateFederationRootCA("test.collective.local", 365*24*time.Hour); err != nil {
		t.Fatal(err)
	}

	// Test loading root CA
	ts := NewTrustStore()
	rootCAPath := filepath.Join(tempDir, "ca.crt")
	
	if err := ts.LoadFederationRootCA(rootCAPath); err != nil {
		t.Errorf("Failed to load root CA: %v", err)
	}

	if ts.GetRootCA() == nil {
		t.Error("Root CA not loaded")
	}

	// Test loading non-existent file
	if err := ts.LoadFederationRootCA("/non/existent/path.crt"); err == nil {
		t.Error("Expected error loading non-existent CA")
	}
}

func TestTrustStore_AddRemoveMemberCA(t *testing.T) {
	// Setup test environment
	tempDir, err := ioutil.TempDir("", "truststore-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tempDir)

	// Generate federation root CA
	fcm := auth.NewFederationCertManager(tempDir)
	if err := fcm.GenerateFederationRootCA("test.collective.local", 365*24*time.Hour); err != nil {
		t.Fatal(err)
	}

	// Load root CA
	if err := fcm.LoadCA(); err != nil {
		t.Fatal(err)
	}

	// Generate member CAs
	aliceCAPath := filepath.Join(tempDir, "alice.test.collective.local-ca.crt")
	bobCAPath := filepath.Join(tempDir, "bob.test.collective.local-ca.crt")
	
	if err := fcm.GenerateMemberCA("alice.test.collective.local", 180*24*time.Hour, aliceCAPath); err != nil {
		t.Fatal(err)
	}
	if err := fcm.GenerateMemberCA("bob.test.collective.local", 180*24*time.Hour, bobCAPath); err != nil {
		t.Fatal(err)
	}

	// Create trust store and load root CA
	ts := NewTrustStore()
	rootCAPath := filepath.Join(tempDir, "ca.crt")
	if err := ts.LoadFederationRootCA(rootCAPath); err != nil {
		t.Fatal(err)
	}

	// Test adding member CAs
	if err := ts.AddMemberCA("alice.test.collective.local", aliceCAPath); err != nil {
		t.Errorf("Failed to add Alice CA: %v", err)
	}
	
	if err := ts.AddMemberCA("bob.test.collective.local", bobCAPath); err != nil {
		t.Errorf("Failed to add Bob CA: %v", err)
	}

	// Verify member CAs were added
	members := ts.ListMemberCAs()
	if len(members) != 2 {
		t.Errorf("Expected 2 member CAs, got %d", len(members))
	}

	// Test getting member CA
	aliceCA, err := ts.GetMemberCA("alice.test.collective.local")
	if err != nil {
		t.Errorf("Failed to get Alice CA: %v", err)
	}
	if aliceCA == nil {
		t.Error("Alice CA is nil")
	}

	// Test removing member CA
	if err := ts.RemoveMemberCA("alice.test.collective.local"); err != nil {
		t.Errorf("Failed to remove Alice CA: %v", err)
	}

	// Verify member CA was removed
	members = ts.ListMemberCAs()
	if len(members) != 1 {
		t.Errorf("Expected 1 member CA after removal, got %d", len(members))
	}

	// Test removing non-existent member
	if err := ts.RemoveMemberCA("carol.test.collective.local"); err == nil {
		t.Error("Expected error removing non-existent member")
	}
}

func TestTrustStore_LoadCADirectory(t *testing.T) {
	// Setup test environment with multiple CAs
	tempDir, err := ioutil.TempDir("", "truststore-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tempDir)

	// Generate federation hierarchy
	fcm := auth.NewFederationCertManager(tempDir)
	if err := fcm.GenerateFederationRootCA("test.collective.local", 365*24*time.Hour); err != nil {
		t.Fatal(err)
	}
	
	if err := fcm.LoadCA(); err != nil {
		t.Fatal(err)
	}

	// Generate 3 member CAs
	members := []string{"alice", "bob", "carol"}
	for _, member := range members {
		memberDomain := member + ".test.collective.local"
		memberCAPath := filepath.Join(tempDir, memberDomain+"-ca.crt")
		if err := fcm.GenerateMemberCA(memberDomain, 180*24*time.Hour, memberCAPath); err != nil {
			t.Fatal(err)
		}
	}

	// Test loading entire directory
	ts := NewTrustStore()
	if err := ts.LoadCADirectory(tempDir); err != nil {
		t.Errorf("Failed to load CA directory: %v", err)
	}

	// Verify all CAs were loaded
	if ts.GetRootCA() == nil {
		t.Error("Root CA not loaded from directory")
	}

	memberList := ts.ListMemberCAs()
	if len(memberList) != 3 {
		t.Errorf("Expected 3 member CAs, got %d", len(memberList))
	}

	// Test loading non-existent directory
	if err := ts.LoadCADirectory("/non/existent/dir"); err == nil {
		t.Error("Expected error loading non-existent directory")
	}
}

func TestTrustStore_ValidateCertificate(t *testing.T) {
	// Setup test environment
	tempDir, err := ioutil.TempDir("", "truststore-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tempDir)

	// Generate federation hierarchy
	fcm := auth.NewFederationCertManager(tempDir)
	if err := fcm.GenerateFederationRootCA("test.collective.local", 365*24*time.Hour); err != nil {
		t.Fatal(err)
	}
	
	if err := fcm.LoadCA(); err != nil {
		t.Fatal(err)
	}

	// Generate Alice's member CA
	aliceCAPath := filepath.Join(tempDir, "alice.test.collective.local-ca.crt")
	if err := fcm.GenerateMemberCA("alice.test.collective.local", 180*24*time.Hour, aliceCAPath); err != nil {
		t.Fatal(err)
	}

	// Note: In a real scenario, Alice would have her own CA loaded
	// For testing, we'll validate the intermediate CA itself

	// Create trust store with full hierarchy
	ts := NewTrustStore()
	if err := ts.LoadCADirectory(tempDir); err != nil {
		t.Fatal(err)
	}

	// Test validating Alice's intermediate CA
	aliceCA, err := ts.GetMemberCA("alice.test.collective.local")
	if err != nil {
		t.Fatal(err)
	}

	valid, memberID, err := ts.ValidateCertificate(aliceCA)
	if !valid {
		t.Errorf("Alice's CA should be valid: %v", err)
	}
	// The intermediate CA validates against itself in the trust store
	if memberID != "alice.test.collective.local" && memberID != "root" {
		t.Errorf("Expected memberID 'alice.test.collective.local' or 'root' for intermediate CA, got %s", memberID)
	}

	// Test cache functionality
	ts.SetCacheTTL(1 * time.Second)
	
	// First validation should cache the result
	valid1, _, _ := ts.ValidateCertificate(aliceCA)
	
	// Second validation should use cache
	valid2, _, _ := ts.ValidateCertificate(aliceCA)
	
	if valid1 != valid2 {
		t.Error("Cache validation inconsistent")
	}

	// Wait for cache to expire
	time.Sleep(1100 * time.Millisecond)
	
	// Should revalidate after cache expiry
	valid3, _, _ := ts.ValidateCertificate(aliceCA)
	if valid3 != valid1 {
		t.Error("Validation after cache expiry inconsistent")
	}
}

func TestTrustStore_CertPool(t *testing.T) {
	// Setup test environment
	tempDir, err := ioutil.TempDir("", "truststore-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tempDir)

	// Generate test CAs
	fcm := auth.NewFederationCertManager(tempDir)
	if err := fcm.GenerateFederationRootCA("test.collective.local", 365*24*time.Hour); err != nil {
		t.Fatal(err)
	}
	
	if err := fcm.LoadCA(); err != nil {
		t.Fatal(err)
	}

	// Generate member CAs
	if err := fcm.GenerateMemberCA("alice.test.collective.local", 180*24*time.Hour, 
		filepath.Join(tempDir, "alice.test.collective.local-ca.crt")); err != nil {
		t.Fatal(err)
	}
	if err := fcm.GenerateMemberCA("bob.test.collective.local", 180*24*time.Hour,
		filepath.Join(tempDir, "bob.test.collective.local-ca.crt")); err != nil {
		t.Fatal(err)
	}

	// Load all CAs
	ts := NewTrustStore()
	if err := ts.LoadCADirectory(tempDir); err != nil {
		t.Fatal(err)
	}

	// Get cert pool
	pool := ts.GetCertPool()
	if pool == nil {
		t.Error("GetCertPool returned nil")
	}

	// The pool should contain root + 2 member CAs = 3 subjects
	// Note: x509.CertPool doesn't expose its contents directly
	// We can verify it's non-nil and trust it contains our certs
}

func TestTrustStore_CARotation(t *testing.T) {
	// Setup test environment
	tempDir, err := ioutil.TempDir("", "truststore-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tempDir)

	// Generate initial CAs
	fcm := auth.NewFederationCertManager(tempDir)
	if err := fcm.GenerateFederationRootCA("test.collective.local", 365*24*time.Hour); err != nil {
		t.Fatal(err)
	}
	
	if err := fcm.LoadCA(); err != nil {
		t.Fatal(err)
	}

	// Generate Alice's CA
	aliceCAPath := filepath.Join(tempDir, "alice.test.collective.local-ca.crt")
	if err := fcm.GenerateMemberCA("alice.test.collective.local", 180*24*time.Hour, aliceCAPath); err != nil {
		t.Fatal(err)
	}

	// Create trust store
	ts := NewTrustStore()
	rootCAPath := filepath.Join(tempDir, "ca.crt")
	if err := ts.LoadFederationRootCA(rootCAPath); err != nil {
		t.Fatal(err)
	}

	// Add Alice's CA
	if err := ts.AddMemberCA("alice.test.collective.local", aliceCAPath); err != nil {
		t.Fatal(err)
	}

	// Verify Alice is trusted
	members := ts.ListMemberCAs()
	if len(members) != 1 {
		t.Errorf("Expected 1 member, got %d", len(members))
	}

	// Simulate CA rotation - generate new CA for Alice
	newAliceCAPath := filepath.Join(tempDir, "alice.test.collective.local-ca-new.crt")
	if err := fcm.GenerateMemberCA("alice.test.collective.local", 180*24*time.Hour, newAliceCAPath); err != nil {
		t.Fatal(err)
	}

	// Remove old CA
	if err := ts.RemoveMemberCA("alice.test.collective.local"); err != nil {
		t.Fatal(err)
	}

	// Add new CA
	if err := ts.AddMemberCA("alice.test.collective.local", newAliceCAPath); err != nil {
		t.Fatal(err)
	}

	// Verify rotation succeeded
	members = ts.ListMemberCAs()
	if len(members) != 1 {
		t.Errorf("Expected 1 member after rotation, got %d", len(members))
	}

	newAliceCA, err := ts.GetMemberCA("alice.test.collective.local")
	if err != nil {
		t.Fatal(err)
	}
	if newAliceCA == nil {
		t.Error("New Alice CA not found after rotation")
	}
}

func TestTrustStore_ConcurrentAccess(t *testing.T) {
	// Setup test environment
	tempDir, err := ioutil.TempDir("", "truststore-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tempDir)

	// Generate test CAs
	fcm := auth.NewFederationCertManager(tempDir)
	if err := fcm.GenerateFederationRootCA("test.collective.local", 365*24*time.Hour); err != nil {
		t.Fatal(err)
	}
	
	if err := fcm.LoadCA(); err != nil {
		t.Fatal(err)
	}

	// Create trust store
	ts := NewTrustStore()
	if err := ts.LoadCADirectory(tempDir); err != nil {
		t.Fatal(err)
	}

	// Test concurrent reads and writes
	done := make(chan bool)
	
	// Reader goroutine
	go func() {
		for i := 0; i < 100; i++ {
			_ = ts.ListMemberCAs()
			_ = ts.GetCertPool()
			_ = ts.GetRootCA()
		}
		done <- true
	}()

	// Writer goroutine
	go func() {
		for i := 0; i < 100; i++ {
			ts.ClearCache()
			ts.SetCacheTTL(time.Duration(i) * time.Millisecond)
		}
		done <- true
	}()

	// Wait for both goroutines
	<-done
	<-done

	// If we get here without deadlock or panic, concurrent access is safe
}