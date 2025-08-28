package coordinator

import (
	"collective/pkg/federation"
	"context"
	"fmt"
	"path/filepath"

	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// PermissionEnforcer wraps coordinator operations with permission checks
type PermissionEnforcer struct {
	*FederationCoordinator
	permissionManager *federation.PermissionManager
}

// NewPermissionEnforcer creates a new permission enforcer
func NewPermissionEnforcer(fc *FederationCoordinator) *PermissionEnforcer {
	return &PermissionEnforcer{
		FederationCoordinator: fc,
		permissionManager:     federation.NewPermissionManager(),
	}
}

// extractCallerIdentity extracts the federated address from gRPC context
func (pe *PermissionEnforcer) extractCallerIdentity(ctx context.Context) (*federation.FederatedAddress, error) {
	// In production, this would extract from mTLS certificate
	// For now, we'll check metadata
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return nil, fmt.Errorf("no metadata in context")
	}
	
	// Look for federated address in metadata
	addresses := md.Get("federated-address")
	if len(addresses) == 0 {
		// Default to local coordinator address
		addr := fmt.Sprintf("coord@%s.%s", pe.memberID, pe.federationDomain)
		return federation.ParseAddress(addr)
	}
	
	return federation.ParseAddress(addresses[0])
}

// checkPermission checks if the caller has the required permission
func (pe *PermissionEnforcer) checkPermission(ctx context.Context, path string, right federation.Right) error {
	caller, err := pe.extractCallerIdentity(ctx)
	if err != nil {
		pe.logger.Warn("Failed to extract caller identity",
			zap.Error(err))
		return status.Error(codes.Unauthenticated, "authentication required")
	}
	
	allowed, err := pe.permissionManager.CheckPermission(path, caller, right)
	if err != nil {
		pe.logger.Error("Permission check failed",
			zap.String("path", path),
			zap.String("caller", caller.String()),
			zap.String("right", string(right)),
			zap.Error(err))
		return status.Error(codes.Internal, "permission check failed")
	}
	
	if !allowed {
		pe.logger.Warn("Permission denied",
			zap.String("path", path),
			zap.String("caller", caller.String()),
			zap.String("right", string(right)))
		return status.Errorf(codes.PermissionDenied, 
			"access denied: %s permission required for %s", right, path)
	}
	
	return nil
}

// File operation permission enforcement

// EnforceStoreFile checks write permission before storing a file
func (pe *PermissionEnforcer) EnforceStoreFile(ctx context.Context, filePath string) error {
	return pe.checkPermission(ctx, filePath, federation.RightWrite)
}

// EnforceRetrieveFile checks read permission before retrieving a file
func (pe *PermissionEnforcer) EnforceRetrieveFile(ctx context.Context, filePath string) error {
	return pe.checkPermission(ctx, filePath, federation.RightRead)
}

// EnforceDeleteFile checks delete permission before deleting a file
func (pe *PermissionEnforcer) EnforceDeleteFile(ctx context.Context, filePath string) error {
	return pe.checkPermission(ctx, filePath, federation.RightDelete)
}

// EnforceListDirectory checks read permission before listing a directory
func (pe *PermissionEnforcer) EnforceListDirectory(ctx context.Context, dirPath string) error {
	return pe.checkPermission(ctx, dirPath, federation.RightRead)
}

// EnforceCreateDirectory checks write permission before creating a directory
func (pe *PermissionEnforcer) EnforceCreateDirectory(ctx context.Context, dirPath string) error {
	// Check parent directory write permission
	parent := filepath.Dir(dirPath)
	return pe.checkPermission(ctx, parent, federation.RightWrite)
}

// DataStore management with permissions

// CreateDataStore creates a new DataStore with the caller as owner
func (pe *PermissionEnforcer) CreateDataStore(ctx context.Context, path string, strategy federation.PlacementStrategy) error {
	caller, err := pe.extractCallerIdentity(ctx)
	if err != nil {
		return status.Error(codes.Unauthenticated, "authentication required")
	}
	
	// Check if caller has write permission on parent
	parent := filepath.Dir(path)
	if parent != "/" && parent != "." {
		if err := pe.checkPermission(ctx, parent, federation.RightWrite); err != nil {
			return err
		}
	}
	
	// Create the DataStore with caller as owner
	if err := pe.permissionManager.CreateDataStore(path, caller, strategy); err != nil {
		pe.logger.Error("Failed to create DataStore",
			zap.String("path", path),
			zap.String("owner", caller.String()),
			zap.Error(err))
		return status.Error(codes.Internal, err.Error())
	}
	
	pe.logger.Info("DataStore created",
		zap.String("path", path),
		zap.String("owner", caller.String()),
		zap.Int("strategy", int(strategy)))
	
	return nil
}

// GrantPermission grants permissions on a DataStore (requires SHARE right)
func (pe *PermissionEnforcer) GrantPermission(ctx context.Context, path string, subject string, rights []federation.Right) error {
	// Check if caller has SHARE permission
	if err := pe.checkPermission(ctx, path, federation.RightShare); err != nil {
		return err
	}
	
	// Parse subject address
	subjectAddr, err := federation.ParseAddress(subject)
	if err != nil {
		return status.Errorf(codes.InvalidArgument, "invalid subject address: %v", err)
	}
	
	// Grant the permission
	if err := pe.permissionManager.GrantPermission(path, subjectAddr, rights, nil); err != nil {
		pe.logger.Error("Failed to grant permission",
			zap.String("path", path),
			zap.String("subject", subject),
			zap.Error(err))
		return status.Error(codes.Internal, err.Error())
	}
	
	pe.logger.Info("Permission granted",
		zap.String("path", path),
		zap.String("subject", subject),
		zap.Any("rights", rights))
	
	return nil
}

// RevokePermission revokes permissions on a DataStore (requires SHARE right or ownership)
func (pe *PermissionEnforcer) RevokePermission(ctx context.Context, path string, subject string) error {
	caller, err := pe.extractCallerIdentity(ctx)
	if err != nil {
		return status.Error(codes.Unauthenticated, "authentication required")
	}
	
	// Check if caller is owner or has SHARE permission
	isOwner := pe.permissionManager.IsOwner(path, caller)
	if !isOwner {
		if err := pe.checkPermission(ctx, path, federation.RightShare); err != nil {
			return err
		}
	}
	
	// Parse subject address
	subjectAddr, err := federation.ParseAddress(subject)
	if err != nil {
		return status.Errorf(codes.InvalidArgument, "invalid subject address: %v", err)
	}
	
	// Revoke the permission
	if err := pe.permissionManager.RevokePermission(path, subjectAddr); err != nil {
		pe.logger.Error("Failed to revoke permission",
			zap.String("path", path),
			zap.String("subject", subject),
			zap.Error(err))
		return status.Error(codes.Internal, err.Error())
	}
	
	pe.logger.Info("Permission revoked",
		zap.String("path", path),
		zap.String("subject", subject))
	
	return nil
}

// GetPermissions returns permissions for a DataStore
func (pe *PermissionEnforcer) GetPermissions(ctx context.Context, path string) ([]federation.Permission, error) {
	// Check if caller has any permission on the DataStore
	if err := pe.checkPermission(ctx, path, federation.RightRead); err != nil {
		return nil, err
	}
	
	perms, err := pe.permissionManager.GetPermissions(path)
	if err != nil {
		return nil, status.Error(codes.NotFound, err.Error())
	}
	
	return perms, nil
}

// InitializeDefaultDataStores creates default DataStores for the federation member
func (pe *PermissionEnforcer) InitializeDefaultDataStores() error {
	// Create local member address
	localAddr, err := federation.ParseAddress(fmt.Sprintf("coord@%s.%s", pe.memberID, pe.federationDomain))
	if err != nil {
		return fmt.Errorf("failed to parse local address: %w", err)
	}
	
	// Create root DataStore
	if err := pe.permissionManager.CreateDataStore("/", localAddr, federation.PlacementHybrid); err != nil {
		// Ignore if already exists
		pe.logger.Debug("Root DataStore might already exist", zap.Error(err))
	}
	
	// Create default DataStores
	defaultStores := map[string]federation.PlacementStrategy{
		"/media":   federation.PlacementMedia,
		"/backups": federation.PlacementBackup,
		"/shared":  federation.PlacementHybrid,
	}
	
	for path, strategy := range defaultStores {
		if err := pe.permissionManager.CreateDataStore(path, localAddr, strategy); err != nil {
			pe.logger.Debug("DataStore might already exist",
				zap.String("path", path),
				zap.Error(err))
		}
	}
	
	// Grant read access to federation members on /shared
	wildcardAddr, _ := federation.ParseAddress("*@*.collective.local")
	pe.permissionManager.GrantPermission("/shared", wildcardAddr, []federation.Right{federation.RightRead}, nil)
	
	pe.logger.Info("Default DataStores initialized",
		zap.String("member", string(pe.memberID)))
	
	return nil
}