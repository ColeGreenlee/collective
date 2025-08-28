package federation

import (
	"testing"
	"time"
)

func TestPermissionManager_CreateDataStore(t *testing.T) {
	pm := NewPermissionManager()

	// Create owner address
	owner, err := ParseAddress("alice@home.collective.local")
	if err != nil {
		t.Fatal(err)
	}

	// Create DataStore
	err = pm.CreateDataStore("/media/movies", owner, PlacementMedia)
	if err != nil {
		t.Errorf("Failed to create DataStore: %v", err)
	}

	// Verify DataStore was created
	ds, err := pm.GetDataStore("/media/movies")
	if err != nil {
		t.Errorf("Failed to get DataStore: %v", err)
	}

	if !ds.Owner.Equal(owner) {
		t.Errorf("Expected owner %s, got %s", owner.String(), ds.Owner.String())
	}

	if ds.Strategy != PlacementMedia {
		t.Errorf("Expected strategy %d, got %d", PlacementMedia, ds.Strategy)
	}

	// Test duplicate creation
	err = pm.CreateDataStore("/media/movies", owner, PlacementMedia)
	if err == nil {
		t.Error("Expected error creating duplicate DataStore")
	}
}

func TestPermissionManager_GrantRevokePermission(t *testing.T) {
	pm := NewPermissionManager()

	// Create DataStore
	owner, _ := ParseAddress("alice@home.collective.local")
	bob, _ := ParseAddress("bob@garage.collective.local")

	pm.CreateDataStore("/shared", owner, PlacementHybrid)

	// Grant permission to Bob
	rights := []Right{RightRead, RightWrite}
	err := pm.GrantPermission("/shared", bob, rights, nil)
	if err != nil {
		t.Errorf("Failed to grant permission: %v", err)
	}

	// Check Bob has permission
	hasRead, _ := pm.CheckPermission("/shared", bob, RightRead)
	if !hasRead {
		t.Error("Bob should have read permission")
	}

	hasWrite, _ := pm.CheckPermission("/shared", bob, RightWrite)
	if !hasWrite {
		t.Error("Bob should have write permission")
	}

	hasDelete, _ := pm.CheckPermission("/shared", bob, RightDelete)
	if hasDelete {
		t.Error("Bob should not have delete permission")
	}

	// Revoke permission
	err = pm.RevokePermission("/shared", bob)
	if err != nil {
		t.Errorf("Failed to revoke permission: %v", err)
	}

	// Check Bob no longer has permission
	hasRead, _ = pm.CheckPermission("/shared", bob, RightRead)
	if hasRead {
		t.Error("Bob should not have read permission after revoke")
	}
}

func TestPermissionManager_WildcardPermissions(t *testing.T) {
	pm := NewPermissionManager()

	// Create DataStore
	owner, _ := ParseAddress("alice@home.collective.local")
	pm.CreateDataStore("/public", owner, PlacementHybrid)

	// Grant wildcard permission for all members
	wildcard, _ := ParseAddress("*@*.collective.local")
	err := pm.GrantPermission("/public", wildcard, []Right{RightRead}, nil)
	if err != nil {
		t.Errorf("Failed to grant wildcard permission: %v", err)
	}

	// Check various members have read access
	bob, _ := ParseAddress("bob@garage.collective.local")
	carol, _ := ParseAddress("carol@house.collective.local")

	hasRead, _ := pm.CheckPermission("/public", bob, RightRead)
	if !hasRead {
		t.Error("Bob should have read permission via wildcard")
	}

	hasRead, _ = pm.CheckPermission("/public", carol, RightRead)
	if !hasRead {
		t.Error("Carol should have read permission via wildcard")
	}

	// But not write permission
	hasWrite, _ := pm.CheckPermission("/public", bob, RightWrite)
	if hasWrite {
		t.Error("Bob should not have write permission")
	}
}

func TestPermissionManager_DirectoryInheritance(t *testing.T) {
	pm := NewPermissionManager()

	// Create parent DataStore
	owner, _ := ParseAddress("alice@home.collective.local")
	pm.CreateDataStore("/data", owner, PlacementHybrid)

	// Grant permission on parent
	bob, _ := ParseAddress("bob@garage.collective.local")
	pm.GrantPermission("/data", bob, []Right{RightRead, RightWrite}, nil)

	// Check permission on subdirectory (should inherit)
	hasRead, _ := pm.CheckPermission("/data/subdir/file.txt", bob, RightRead)
	if !hasRead {
		t.Error("Bob should have read permission on subdirectory via inheritance")
	}

	hasWrite, _ := pm.CheckPermission("/data/subdir/nested/deep.txt", bob, RightWrite)
	if !hasWrite {
		t.Error("Bob should have write permission on nested path via inheritance")
	}
}

func TestPermissionManager_TimeLimitedPermissions(t *testing.T) {
	pm := NewPermissionManager()
	// Disable cache for this test to ensure we check actual permission expiry
	pm.SetCacheTTL(0)

	// Create DataStore
	owner, _ := ParseAddress("alice@home.collective.local")
	pm.CreateDataStore("/temp", owner, PlacementHybrid)

	// Grant time-limited permission (expires in 100ms)
	bob, _ := ParseAddress("bob@garage.collective.local")
	expiry := time.Now().Add(100 * time.Millisecond)
	err := pm.GrantPermission("/temp", bob, []Right{RightRead}, &expiry)
	if err != nil {
		t.Errorf("Failed to grant time-limited permission: %v", err)
	}

	// Check permission is valid initially
	hasRead, _ := pm.CheckPermission("/temp", bob, RightRead)
	if !hasRead {
		t.Error("Bob should have read permission before expiry")
	}

	// Wait for expiry
	time.Sleep(150 * time.Millisecond)

	// Check permission has expired
	hasRead, _ = pm.CheckPermission("/temp", bob, RightRead)
	if hasRead {
		t.Error("Bob should not have read permission after expiry")
	}
}

func TestPermissionManager_OwnerPermissions(t *testing.T) {
	pm := NewPermissionManager()

	// Create DataStore
	owner, _ := ParseAddress("alice@home.collective.local")
	pm.CreateDataStore("/private", owner, PlacementHybrid)

	// Owner should have all permissions
	rights := []Right{RightRead, RightWrite, RightDelete, RightShare}
	for _, right := range rights {
		has, _ := pm.CheckPermission("/private", owner, right)
		if !has {
			t.Errorf("Owner should have %s permission", right)
		}
	}

	// Check IsOwner
	if !pm.IsOwner("/private", owner) {
		t.Error("IsOwner should return true for owner")
	}

	bob, _ := ParseAddress("bob@garage.collective.local")
	if pm.IsOwner("/private", bob) {
		t.Error("IsOwner should return false for non-owner")
	}
}

func TestPermissionManager_TransferOwnership(t *testing.T) {
	pm := NewPermissionManager()

	// Create DataStore
	alice, _ := ParseAddress("alice@home.collective.local")
	bob, _ := ParseAddress("bob@garage.collective.local")

	pm.CreateDataStore("/transfer", alice, PlacementHybrid)

	// Transfer ownership to Bob
	err := pm.TransferOwnership("/transfer", alice, bob)
	if err != nil {
		t.Errorf("Failed to transfer ownership: %v", err)
	}

	// Verify Bob is now owner
	if !pm.IsOwner("/transfer", bob) {
		t.Error("Bob should be owner after transfer")
	}

	if pm.IsOwner("/transfer", alice) {
		t.Error("Alice should not be owner after transfer")
	}

	// Bob should have all rights
	hasShare, _ := pm.CheckPermission("/transfer", bob, RightShare)
	if !hasShare {
		t.Error("New owner should have share permission")
	}
}

func TestPermissionManager_Cache(t *testing.T) {
	pm := NewPermissionManager()
	pm.SetCacheTTL(100 * time.Millisecond)

	// Create DataStore and grant permission
	owner, _ := ParseAddress("alice@home.collective.local")
	bob, _ := ParseAddress("bob@garage.collective.local")
	carol, _ := ParseAddress("carol@house.collective.local")

	pm.CreateDataStore("/cached", owner, PlacementHybrid)
	pm.GrantPermission("/cached", bob, []Right{RightRead}, nil)

	// First check (will cache the result)
	hasRead1, _ := pm.CheckPermission("/cached", bob, RightRead)
	if !hasRead1 {
		t.Error("Bob should have read permission")
	}

	// Second check should use cache (not recompute)
	hasRead2, _ := pm.CheckPermission("/cached", bob, RightRead)
	if !hasRead2 {
		t.Error("Bob should have read permission from cache")
	}

	// Check for carol (different cache key)
	hasRead3, _ := pm.CheckPermission("/cached", carol, RightRead)
	if hasRead3 {
		t.Error("Carol should not have read permission")
	}

	// Wait for cache to expire
	time.Sleep(150 * time.Millisecond)

	// After cache expiry, permission check should recompute
	hasRead4, _ := pm.CheckPermission("/cached", bob, RightRead)
	if !hasRead4 {
		t.Error("Bob should still have read permission after cache expiry")
	}
}

func TestPermissionManager_ListDataStores(t *testing.T) {
	pm := NewPermissionManager()

	// Create multiple DataStores
	owner, _ := ParseAddress("alice@home.collective.local")

	paths := []string{"/media", "/backups", "/shared"}
	for _, path := range paths {
		pm.CreateDataStore(path, owner, PlacementHybrid)
	}

	// List all DataStores
	stores := pm.ListDataStores()
	if len(stores) != len(paths) {
		t.Errorf("Expected %d DataStores, got %d", len(paths), len(stores))
	}

	// Verify all paths are present
	foundPaths := make(map[string]bool)
	for _, ds := range stores {
		foundPaths[ds.Path] = true
	}

	for _, path := range paths {
		if !foundPaths[path] {
			t.Errorf("DataStore %s not found in list", path)
		}
	}
}

func TestPermissionManager_GetPermissions(t *testing.T) {
	pm := NewPermissionManager()

	// Create DataStore
	owner, _ := ParseAddress("alice@home.collective.local")
	bob, _ := ParseAddress("bob@garage.collective.local")
	carol, _ := ParseAddress("carol@house.collective.local")

	pm.CreateDataStore("/multi", owner, PlacementHybrid)

	// Grant different permissions
	pm.GrantPermission("/multi", bob, []Right{RightRead}, nil)
	pm.GrantPermission("/multi", carol, []Right{RightRead, RightWrite}, nil)

	// Get all permissions
	perms, err := pm.GetPermissions("/multi")
	if err != nil {
		t.Errorf("Failed to get permissions: %v", err)
	}

	// Should have 3 permissions (owner + bob + carol)
	if len(perms) != 3 {
		t.Errorf("Expected 3 permissions, got %d", len(perms))
	}

	// Verify permissions are correct
	foundBob := false
	foundCarol := false
	for _, perm := range perms {
		if perm.Subject.Equal(bob) {
			foundBob = true
			if len(perm.Rights) != 1 || perm.Rights[0] != RightRead {
				t.Error("Bob's permissions are incorrect")
			}
		}
		if perm.Subject.Equal(carol) {
			foundCarol = true
			if len(perm.Rights) != 2 {
				t.Error("Carol's permissions are incorrect")
			}
		}
	}

	if !foundBob || !foundCarol {
		t.Error("Not all permissions were found")
	}
}

func TestPermissionManager_ImportExport(t *testing.T) {
	pm1 := NewPermissionManager()

	// Create some DataStores
	owner, _ := ParseAddress("alice@home.collective.local")
	pm1.CreateDataStore("/export1", owner, PlacementMedia)
	pm1.CreateDataStore("/export2", owner, PlacementBackup)

	// Export DataStores
	exported := pm1.ExportDataStores()
	if len(exported) != 2 {
		t.Errorf("Expected 2 exported DataStores, got %d", len(exported))
	}

	// Import into new manager
	pm2 := NewPermissionManager()
	err := pm2.ImportDataStores(exported)
	if err != nil {
		t.Errorf("Failed to import DataStores: %v", err)
	}

	// Verify imported DataStores
	ds1, err := pm2.GetDataStore("/export1")
	if err != nil || ds1.Strategy != PlacementMedia {
		t.Error("First DataStore not imported correctly")
	}

	ds2, err := pm2.GetDataStore("/export2")
	if err != nil || ds2.Strategy != PlacementBackup {
		t.Error("Second DataStore not imported correctly")
	}
}
