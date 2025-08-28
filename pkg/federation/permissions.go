package federation

import (
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// PermissionManager handles DataStore access control
type PermissionManager struct {
	mu sync.RWMutex
	
	// DataStores with their permissions
	dataStores map[string]*DataStore // path -> datastore
	
	// Permission cache for performance
	cache      map[string]*permissionCacheEntry
	cacheTTL   time.Duration
}

// permissionCacheEntry holds cached permission check results
type permissionCacheEntry struct {
	allowed   bool
	rights    []Right
	expiresAt time.Time
}

// NewPermissionManager creates a new permission manager
func NewPermissionManager() *PermissionManager {
	return &PermissionManager{
		dataStores: make(map[string]*DataStore),
		cache:      make(map[string]*permissionCacheEntry),
		cacheTTL:   1 * time.Minute,
	}
}

// CreateDataStore creates a new DataStore with owner permissions
func (pm *PermissionManager) CreateDataStore(path string, owner *FederatedAddress, strategy PlacementStrategy) error {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	
	// Normalize path
	path = filepath.Clean(path)
	
	// Check if DataStore already exists
	if _, exists := pm.dataStores[path]; exists {
		return fmt.Errorf("datastore %s already exists", path)
	}
	
	// Create DataStore with owner having all rights
	ds := &DataStore{
		Path:     path,
		Owner:    owner,
		Replicas: 2, // Default replication
		Strategy: strategy,
		Permissions: []Permission{
			{
				Subject: owner,
				Rights:  []Right{RightRead, RightWrite, RightDelete, RightShare},
			},
		},
		Metadata: make(map[string]string),
	}
	
	pm.dataStores[path] = ds
	pm.clearCache() // Clear cache when permissions change
	
	return nil
}

// GrantPermission grants access rights to a subject
func (pm *PermissionManager) GrantPermission(path string, subject *FederatedAddress, rights []Right, validUntil *time.Time) error {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	
	path = filepath.Clean(path)
	
	ds, exists := pm.dataStores[path]
	if !exists {
		return fmt.Errorf("datastore %s not found", path)
	}
	
	// Check if permission already exists for this subject
	for i, perm := range ds.Permissions {
		if perm.Subject.Equal(subject) {
			// Update existing permission
			ds.Permissions[i].Rights = rights
			ds.Permissions[i].ValidUntil = validUntil
			pm.clearCache()
			return nil
		}
	}
	
	// Add new permission
	ds.Permissions = append(ds.Permissions, Permission{
		Subject:    subject,
		Rights:     rights,
		ValidUntil: validUntil,
	})
	
	pm.clearCache()
	return nil
}

// RevokePermission removes access rights from a subject
func (pm *PermissionManager) RevokePermission(path string, subject *FederatedAddress) error {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	
	path = filepath.Clean(path)
	
	ds, exists := pm.dataStores[path]
	if !exists {
		return fmt.Errorf("datastore %s not found", path)
	}
	
	// Remove permission for subject
	newPerms := []Permission{}
	for _, perm := range ds.Permissions {
		if !perm.Subject.Equal(subject) {
			newPerms = append(newPerms, perm)
		}
	}
	
	ds.Permissions = newPerms
	pm.clearCache()
	
	return nil
}

// CheckPermission checks if a subject has specific rights on a path
func (pm *PermissionManager) CheckPermission(path string, subject *FederatedAddress, right Right) (bool, error) {
	// Check cache first
	cacheKey := fmt.Sprintf("%s:%s:%s", path, subject.String(), right)
	
	pm.mu.RLock()
	if entry, exists := pm.cache[cacheKey]; exists {
		if time.Now().Before(entry.expiresAt) {
			pm.mu.RUnlock()
			return entry.allowed, nil
		}
	}
	pm.mu.RUnlock()
	
	// Perform actual permission check
	pm.mu.Lock()
	defer pm.mu.Unlock()
	
	path = filepath.Clean(path)
	
	// Find the DataStore (check exact match and parent paths)
	ds := pm.findDataStore(path)
	if ds == nil {
		// No DataStore found, deny access
		pm.updateCache(cacheKey, false, nil)
		return false, nil
	}
	
	// Check permissions
	allowed, rights := pm.checkDataStorePermission(ds, subject, right)
	
	// Update cache
	pm.updateCache(cacheKey, allowed, rights)
	
	return allowed, nil
}

// findDataStore finds the DataStore for a path (with inheritance)
func (pm *PermissionManager) findDataStore(path string) *DataStore {
	// Check exact match
	if ds, exists := pm.dataStores[path]; exists {
		return ds
	}
	
	// Check parent paths for inheritance
	parent := path
	for parent != "/" && parent != "." {
		parent = filepath.Dir(parent)
		if ds, exists := pm.dataStores[parent]; exists {
			return ds // Inherit from parent DataStore
		}
	}
	
	// Check root
	if ds, exists := pm.dataStores["/"]; exists {
		return ds
	}
	
	return nil
}

// checkDataStorePermission checks if subject has right in DataStore
func (pm *PermissionManager) checkDataStorePermission(ds *DataStore, subject *FederatedAddress, right Right) (bool, []Right) {
	now := time.Now()
	
	for _, perm := range ds.Permissions {
		// Check if permission has expired
		if perm.ValidUntil != nil && now.After(*perm.ValidUntil) {
			continue
		}
		
		// Check if subject matches (including wildcards)
		if pm.subjectMatches(perm.Subject, subject) {
			// Check if right is granted
			for _, r := range perm.Rights {
				if r == right {
					return true, perm.Rights
				}
			}
		}
	}
	
	return false, nil
}

// subjectMatches checks if a permission subject matches the given subject
func (pm *PermissionManager) subjectMatches(permSubject, subject *FederatedAddress) bool {
	// Exact match
	if permSubject.Equal(subject) {
		return true
	}
	
	// Wildcard matching: *@*.collective matches any@any.collective
	if permSubject.LocalPart == "*" {
		// Match any local part in the same domain
		if permSubject.Domain == subject.Domain {
			return true
		}
		
		// Match any domain with pattern
		if strings.HasPrefix(permSubject.Domain, "*.") {
			domainSuffix := permSubject.Domain[1:] // Remove *
			if strings.HasSuffix(subject.Domain, domainSuffix) {
				return true
			}
		}
	}
	
	return false
}

// GetDataStore returns a DataStore by path
func (pm *PermissionManager) GetDataStore(path string) (*DataStore, error) {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	
	path = filepath.Clean(path)
	
	ds, exists := pm.dataStores[path]
	if !exists {
		return nil, fmt.Errorf("datastore %s not found", path)
	}
	
	return ds, nil
}

// ListDataStores returns all DataStores
func (pm *PermissionManager) ListDataStores() []*DataStore {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	
	stores := make([]*DataStore, 0, len(pm.dataStores))
	for _, ds := range pm.dataStores {
		stores = append(stores, ds)
	}
	
	return stores
}

// GetPermissions returns all permissions for a DataStore
func (pm *PermissionManager) GetPermissions(path string) ([]Permission, error) {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	
	path = filepath.Clean(path)
	
	ds, exists := pm.dataStores[path]
	if !exists {
		return nil, fmt.Errorf("datastore %s not found", path)
	}
	
	return ds.Permissions, nil
}

// IsOwner checks if a subject owns a DataStore
func (pm *PermissionManager) IsOwner(path string, subject *FederatedAddress) bool {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	
	path = filepath.Clean(path)
	
	ds, exists := pm.dataStores[path]
	if !exists {
		return false
	}
	
	return ds.Owner.Equal(subject)
}

// TransferOwnership transfers DataStore ownership
func (pm *PermissionManager) TransferOwnership(path string, currentOwner, newOwner *FederatedAddress) error {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	
	path = filepath.Clean(path)
	
	ds, exists := pm.dataStores[path]
	if !exists {
		return fmt.Errorf("datastore %s not found", path)
	}
	
	// Verify current owner
	if !ds.Owner.Equal(currentOwner) {
		return fmt.Errorf("not the owner of datastore %s", path)
	}
	
	// Transfer ownership
	ds.Owner = newOwner
	
	// Grant all rights to new owner if not already present
	hasNewOwnerPerm := false
	for i, perm := range ds.Permissions {
		if perm.Subject.Equal(newOwner) {
			ds.Permissions[i].Rights = []Right{RightRead, RightWrite, RightDelete, RightShare}
			hasNewOwnerPerm = true
			break
		}
	}
	
	if !hasNewOwnerPerm {
		ds.Permissions = append(ds.Permissions, Permission{
			Subject: newOwner,
			Rights:  []Right{RightRead, RightWrite, RightDelete, RightShare},
		})
	}
	
	pm.clearCache()
	return nil
}

// Helper methods

func (pm *PermissionManager) updateCache(key string, allowed bool, rights []Right) {
	pm.cache[key] = &permissionCacheEntry{
		allowed:   allowed,
		rights:    rights,
		expiresAt: time.Now().Add(pm.cacheTTL),
	}
}

func (pm *PermissionManager) clearCache() {
	pm.cache = make(map[string]*permissionCacheEntry)
}

// SetCacheTTL sets the cache TTL
func (pm *PermissionManager) SetCacheTTL(ttl time.Duration) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	pm.cacheTTL = ttl
	pm.clearCache()
}

// ImportDataStores bulk imports DataStores (for testing/migration)
func (pm *PermissionManager) ImportDataStores(stores []*DataStore) error {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	
	for _, ds := range stores {
		if ds == nil {
			continue
		}
		path := filepath.Clean(ds.Path)
		pm.dataStores[path] = ds
	}
	
	pm.clearCache()
	return nil
}

// ExportDataStores exports all DataStores (for persistence/replication)
func (pm *PermissionManager) ExportDataStores() []*DataStore {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	
	stores := make([]*DataStore, 0, len(pm.dataStores))
	for _, ds := range pm.dataStores {
		// Create a copy to avoid external modification
		dsCopy := *ds
		stores = append(stores, &dsCopy)
	}
	
	return stores
}