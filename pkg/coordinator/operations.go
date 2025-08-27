package coordinator

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"collective/pkg/protocol"
	"collective/pkg/types"

	"go.uber.org/zap"
)

// =============================================================================
// Directory Operations
// =============================================================================

// CreateDirectory creates a new directory in the collective
func (c *Coordinator) CreateDirectory(ctx context.Context, req *protocol.CreateDirectoryRequest) (*protocol.CreateDirectoryResponse, error) {
	c.logger.Debug("CreateDirectory request", zap.String("path", req.Path))

	// Validate path
	if req.Path == "" || req.Path == "/" {
		return &protocol.CreateDirectoryResponse{
			Success: false,
			Message: "Invalid directory path",
		}, nil
	}

	// Normalize path
	path := filepath.Clean(req.Path)
	if !filepath.IsAbs(path) {
		path = "/" + path
	}

	c.directoryMutex.Lock()
	defer c.directoryMutex.Unlock()

	// Check if directory already exists
	if _, exists := c.directories[path]; exists {
		return &protocol.CreateDirectoryResponse{
			Success: false,
			Message: "Directory already exists",
		}, nil
	}

	// Check if parent exists
	parent := getParentPath(path)
	if parent != "" && parent != "/" {
		if _, parentExists := c.directories[parent]; !parentExists {
			return &protocol.CreateDirectoryResponse{
				Success: false,
				Message: "Parent directory does not exist",
			}, nil
		}
		// Add to parent's children
		parentDir := c.directories[parent]
		parentDir.Children = append(parentDir.Children, path)
	}

	// Create the directory
	c.directories[path] = &types.Directory{
		Path:     path,
		Parent:   getParentPath(path),
		Children: []string{},
		Mode:     0755,
		Modified: time.Now(),
		Owner:    c.memberID,
	}

	c.logger.Info("Directory created", zap.String("path", path))

	return &protocol.CreateDirectoryResponse{
		Success: true,
		Message: "Directory created successfully",
	}, nil
}

// ListDirectory lists contents of a directory
func (c *Coordinator) ListDirectory(ctx context.Context, req *protocol.ListDirectoryRequest) (*protocol.ListDirectoryResponse, error) {
	c.logger.Debug("ListDirectory request", zap.String("path", req.Path))

	// Normalize path
	path := filepath.Clean(req.Path)
	if path == "" {
		path = "/"
	}
	if !filepath.IsAbs(path) {
		path = "/" + path
	}

	c.directoryMutex.RLock()
	defer c.directoryMutex.RUnlock()

	dir, exists := c.directories[path]
	if !exists {
		return &protocol.ListDirectoryResponse{
			Success: false,
		}, nil
	}

	entries := []*protocol.DirectoryEntry{}

	// Add subdirectories
	for _, childPath := range dir.Children {
		if childDir, exists := c.directories[childPath]; exists {
			entries = append(entries, &protocol.DirectoryEntry{
				Path:        childDir.Path,
				IsDirectory: true,
				Mode:        uint32(childDir.Mode),
				ModifiedTime: childDir.Modified.Unix(),
				Owner:       string(childDir.Owner),
			})
		}
	}

	// Add files in this directory
	for filePath, fileEntry := range c.fileEntries {
		if getParentPath(filePath) == path {
			entries = append(entries, &protocol.DirectoryEntry{
				Path:        filePath,
				IsDirectory: false,
				Size:        fileEntry.Size,
				Mode:        uint32(fileEntry.Mode),
				ModifiedTime: fileEntry.Modified.Unix(),
				Owner:       string(fileEntry.Owner),
			})
		}
	}

	return &protocol.ListDirectoryResponse{
		Success: true,
		Entries: entries,
	}, nil
}

// DeleteDirectory deletes a directory
func (c *Coordinator) DeleteDirectory(ctx context.Context, req *protocol.DeleteDirectoryRequest) (*protocol.DeleteDirectoryResponse, error) {
	c.logger.Debug("DeleteDirectory request",
		zap.String("path", req.Path),
		zap.Bool("recursive", req.Recursive))

	// Validate path - don't allow deleting root
	if req.Path == "" || req.Path == "/" {
		return &protocol.DeleteDirectoryResponse{
			Success: false,
			Message: "Cannot delete root directory",
		}, nil
	}

	// Normalize path
	path := filepath.Clean(req.Path)
	if !filepath.IsAbs(path) {
		path = "/" + path
	}

	c.directoryMutex.Lock()
	defer c.directoryMutex.Unlock()

	// Check if directory exists
	dir, exists := c.directories[path]
	if !exists {
		return &protocol.DeleteDirectoryResponse{
			Success: false,
			Message: "Directory does not exist",
		}, nil
	}

	// Check if directory has children
	hasChildren := len(dir.Children) > 0
	// Also check for files
	for filePath := range c.fileEntries {
		if getParentPath(filePath) == path {
			hasChildren = true
			break
		}
	}

	if hasChildren && !req.Recursive {
		return &protocol.DeleteDirectoryResponse{
			Success: false,
			Message: "Directory is not empty",
		}, nil
	}

	// If recursive, delete all contents
	if req.Recursive {
		if err := c.removeDirectoryRecursive(path); err != nil {
			return &protocol.DeleteDirectoryResponse{
				Success: false,
				Message: fmt.Sprintf("Failed to delete directory: %v", err),
			}, nil
		}
	}

	// Remove from parent's children
	parent := getParentPath(path)
	if parent != "" {
		if parentDir, exists := c.directories[parent]; exists {
			newChildren := []string{}
			for _, child := range parentDir.Children {
				if child != path {
					newChildren = append(newChildren, child)
				}
			}
			parentDir.Children = newChildren
		}
	}

	// Delete the directory
	delete(c.directories, path)

	c.logger.Info("Directory deleted",
		zap.String("path", path),
		zap.Bool("recursive", req.Recursive))

	return &protocol.DeleteDirectoryResponse{
		Success: true,
		Message: "Directory deleted successfully",
	}, nil
}

// StatEntry returns information about a file or directory
func (c *Coordinator) StatEntry(ctx context.Context, req *protocol.StatEntryRequest) (*protocol.StatEntryResponse, error) {
	c.logger.Debug("StatEntry request", zap.String("path", req.Path))

	// Normalize path
	path := filepath.Clean(req.Path)
	if path == "" {
		path = "/"
	}
	if !filepath.IsAbs(path) {
		path = "/" + path
	}

	c.directoryMutex.RLock()
	defer c.directoryMutex.RUnlock()

	// Check if it's a directory
	if dir, exists := c.directories[path]; exists {
		return &protocol.StatEntryResponse{
			Success: true,
			Entry: &protocol.DirectoryEntry{
				Path:        dir.Path,
				IsDirectory: true,
				Mode:        uint32(dir.Mode),
				ModifiedTime: dir.Modified.Unix(),
				Owner:       string(dir.Owner),
			},
		}, nil
	}

	// Check if it's a file
	if fileEntry, exists := c.fileEntries[path]; exists {
		return &protocol.StatEntryResponse{
			Success: true,
			Entry: &protocol.DirectoryEntry{
				Path:        path,
				IsDirectory: false,
				Size:        fileEntry.Size,
				Mode:        uint32(fileEntry.Mode),
				ModifiedTime: fileEntry.Modified.Unix(),
				Owner:       string(fileEntry.Owner),
			},
		}, nil
	}

	return &protocol.StatEntryResponse{
		Success: false,
	}, nil
}

// MoveEntry moves or renames a file or directory
func (c *Coordinator) MoveEntry(ctx context.Context, req *protocol.MoveEntryRequest) (*protocol.MoveEntryResponse, error) {
	c.logger.Debug("MoveEntry request",
		zap.String("old_path", req.OldPath),
		zap.String("new_path", req.NewPath))

	// Normalize paths
	oldPath := filepath.Clean(req.OldPath)
	newPath := filepath.Clean(req.NewPath)
	if !filepath.IsAbs(oldPath) {
		oldPath = "/" + oldPath
	}
	if !filepath.IsAbs(newPath) {
		newPath = "/" + newPath
	}

	// Don't allow moving root
	if oldPath == "/" {
		return &protocol.MoveEntryResponse{
			Success: false,
			Message: "Cannot move root directory",
		}, nil
	}

	c.directoryMutex.Lock()
	defer c.directoryMutex.Unlock()

	// Check if source is a directory
	if dir, exists := c.directories[oldPath]; exists {
		// Check if destination already exists
		if _, exists := c.directories[newPath]; exists {
			return &protocol.MoveEntryResponse{
				Success: false,
				Message: "Destination directory already exists",
			}, nil
		}
		if _, exists := c.fileEntries[newPath]; exists {
			return &protocol.MoveEntryResponse{
				Success: false,
				Message: "A file exists at the destination path",
			}, nil
		}

		// Check that we're not moving a directory into itself
		if strings.HasPrefix(newPath, oldPath+"/") {
			return &protocol.MoveEntryResponse{
				Success: false,
				Message: "Cannot move directory into itself",
			}, nil
		}

		// Update parent references
		oldParent := getParentPath(oldPath)
		newParent := getParentPath(newPath)

		// Remove from old parent
		if oldParent != "" {
			if parentDir, exists := c.directories[oldParent]; exists {
				newChildren := []string{}
				for _, child := range parentDir.Children {
					if child != oldPath {
						newChildren = append(newChildren, child)
					}
				}
				parentDir.Children = newChildren
			}
		}

		// Add to new parent
		if newParent != "" && newParent != "/" {
			// Ensure new parent exists
			if _, exists := c.directories[newParent]; !exists {
				return &protocol.MoveEntryResponse{
					Success: false,
					Message: "Destination parent directory does not exist",
				}, nil
			}
			if parentDir, exists := c.directories[newParent]; exists {
				parentDir.Children = append(parentDir.Children, newPath)
			}
		}

		// Move the directory
		c.directories[newPath] = dir
		dir.Path = newPath
		delete(c.directories, oldPath)

		// Update all child paths recursively
		c.updateChildPaths(oldPath, newPath, dir.Children)

		c.logger.Info("Directory moved",
			zap.String("from", oldPath),
			zap.String("to", newPath))

		return &protocol.MoveEntryResponse{
			Success: true,
			Message: "Directory moved successfully",
		}, nil
	}

	// Check if source is a file
	if _, exists := c.fileEntries[oldPath]; exists {
		return c.moveFile(oldPath, newPath)
	}

	return &protocol.MoveEntryResponse{
		Success: false,
		Message: "Source path not found",
	}, nil
}

// =============================================================================
// Helper functions
// =============================================================================

func (c *Coordinator) moveFile(sourcePath, destPath string) (*protocol.MoveEntryResponse, error) {
	// Check if destination already exists
	if _, exists := c.fileEntries[destPath]; exists {
		return &protocol.MoveEntryResponse{
			Success: false,
			Message: "Destination file already exists",
		}, nil
	}
	if _, exists := c.directories[destPath]; exists {
		return &protocol.MoveEntryResponse{
			Success: false,
			Message: "A directory exists at the destination path",
		}, nil
	}

	// Get file entry
	fileEntry := c.fileEntries[sourcePath]

	// Update parent directory references
	newParent := getParentPath(destPath)

	// Ensure new parent exists
	if newParent != "" && newParent != "/" {
		if _, exists := c.directories[newParent]; !exists {
			return &protocol.MoveEntryResponse{
				Success: false,
				Message: "Destination parent directory does not exist",
			}, nil
		}
	}

	// Move file entry
	c.fileEntries[destPath] = fileEntry
	delete(c.fileEntries, sourcePath)

	// Update chunk mappings
	c.chunkMutex.Lock()
	if chunks, exists := c.fileChunks[sourcePath]; exists {
		c.fileChunks[destPath] = chunks
		delete(c.fileChunks, sourcePath)
	}
	c.chunkMutex.Unlock()

	// Update file locks if any
	c.fileLockMutex.Lock()
	if lock, exists := c.fileLocks[sourcePath]; exists {
		c.fileLocks[destPath] = lock
		delete(c.fileLocks, sourcePath)
	}
	c.fileLockMutex.Unlock()

	c.logger.Info("File moved",
		zap.String("from", sourcePath),
		zap.String("to", destPath))

	return &protocol.MoveEntryResponse{
		Success: true,
		Message: "File moved successfully",
	}, nil
}

func (c *Coordinator) removeDirectoryRecursive(path string) error {
	dir, exists := c.directories[path]
	if !exists {
		return fmt.Errorf("directory not found: %s", path)
	}

	// Delete all child directories
	for _, childPath := range dir.Children {
		if _, isDir := c.directories[childPath]; isDir {
			if err := c.removeDirectoryRecursive(childPath); err != nil {
				return err
			}
		}
	}

	// Delete all files in this directory
	for filePath := range c.fileEntries {
		if getParentPath(filePath) == path {
			delete(c.fileEntries, filePath)
			// Also remove chunk mappings
			c.chunkMutex.Lock()
			delete(c.fileChunks, filePath)
			c.chunkMutex.Unlock()
		}
	}

	// Delete the directory itself
	delete(c.directories, path)
	return nil
}

func (c *Coordinator) updateChildPaths(oldBase, newBase string, children []string) {
	for i, childPath := range children {
		if strings.HasPrefix(childPath, oldBase+"/") {
			newChildPath := strings.Replace(childPath, oldBase, newBase, 1)
			children[i] = newChildPath

			// Update the actual entry
			if dir, exists := c.directories[childPath]; exists {
				c.directories[newChildPath] = dir
				dir.Path = newChildPath
				delete(c.directories, childPath)
				// Recursively update its children
				c.updateChildPaths(childPath, newChildPath, dir.Children)
			}
			// Also check files
			if fileEntry, exists := c.fileEntries[childPath]; exists {
				c.fileEntries[newChildPath] = fileEntry
				delete(c.fileEntries, childPath)
				// Update chunk mappings
				c.chunkMutex.Lock()
				if chunks, exists := c.fileChunks[childPath]; exists {
					c.fileChunks[newChildPath] = chunks
					delete(c.fileChunks, childPath)
				}
				c.chunkMutex.Unlock()
			}
		}
	}
}

// getParentPath returns the parent directory of a path
func getParentPath(path string) string {
	if path == "/" || path == "" {
		return ""
	}
	parent := filepath.Dir(path)
	if parent == "." {
		return "/"
	}
	return parent
}

// initializeRootDirectory creates the root directory if it doesn't exist
func (c *Coordinator) initializeRootDirectory() {
	c.directoryMutex.Lock()
	defer c.directoryMutex.Unlock()

	if _, exists := c.directories["/"]; !exists {
		c.directories["/"] = &types.Directory{
			Path:     "/",
			Parent:   "",
			Children: []string{},
			Mode:     0755,
			Modified: time.Now(),
			Owner:    c.memberID,
		}
		c.logger.Info("Root directory initialized")
	}
}

// getFileLock returns or creates a lock for a specific file path
func (c *Coordinator) getFileLock(path string) *sync.RWMutex {
	c.fileLockMutex.Lock()
	defer c.fileLockMutex.Unlock()

	if lock, exists := c.fileLocks[path]; exists {
		return lock
	}

	lock := &sync.RWMutex{}
	c.fileLocks[path] = lock
	return lock
}