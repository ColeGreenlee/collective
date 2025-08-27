package coordinator

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"collective/pkg/protocol"
	"collective/pkg/types"

	"go.uber.org/zap"
)

// CreateDirectory creates a new directory
func (c *Coordinator) CreateDirectory(ctx context.Context, req *protocol.CreateDirectoryRequest) (*protocol.CreateDirectoryResponse, error) {
	c.logger.Info("CreateDirectory request", zap.String("path", req.Path))

	c.directoryMutex.Lock()
	defer c.directoryMutex.Unlock()

	// Check if directory already exists
	if _, exists := c.directories[req.Path]; exists {
		return &protocol.CreateDirectoryResponse{
			Success: false,
			Message: "Directory already exists",
		}, nil
	}

	// Check if parent exists (except for root)
	if req.Path != "/" {
		parent := getParentPath(req.Path)
		if parent != "" && parent != "/" {
			if _, parentExists := c.directories[parent]; !parentExists {
				return &protocol.CreateDirectoryResponse{
					Success: false,
					Message: "Parent directory does not exist",
				}, nil
			}
		}
	}

	// Create directory entry
	mode := os.FileMode(0755)
	if req.Mode != 0 {
		mode = os.FileMode(req.Mode)
	}

	dir := &types.Directory{
		Path:     req.Path,
		Parent:   getParentPath(req.Path),
		Children: []string{},
		Mode:     mode,
		Modified: time.Now(),
		Owner:    c.memberID,
	}

	c.directories[req.Path] = dir

	// Update parent's children list
	if dir.Parent != "" {
		if parentDir, exists := c.directories[dir.Parent]; exists {
			parentDir.Children = append(parentDir.Children, req.Path)
			parentDir.Modified = time.Now()
		}
	}

	c.logger.Info("Directory created successfully", zap.String("path", req.Path))

	return &protocol.CreateDirectoryResponse{
		Success: true,
		Message: "Directory created successfully",
	}, nil
}

// ListDirectory lists the contents of a directory
func (c *Coordinator) ListDirectory(ctx context.Context, req *protocol.ListDirectoryRequest) (*protocol.ListDirectoryResponse, error) {
	c.logger.Debug("ListDirectory request", zap.String("path", req.Path))

	c.directoryMutex.RLock()
	defer c.directoryMutex.RUnlock()

	dir, exists := c.directories[req.Path]
	if !exists {
		return &protocol.ListDirectoryResponse{
			Success: false,
			Entries: nil,
		}, nil
	}

	var entries []*protocol.DirectoryEntry

	// Add subdirectories
	for _, childPath := range dir.Children {
		if childDir, exists := c.directories[childPath]; exists {
			// It's a directory
			name := getBaseName(childPath)
			entries = append(entries, &protocol.DirectoryEntry{
				Name:         name,
				Path:         childPath,
				IsDirectory:  true,
				Size:         0,
				Mode:         uint32(childDir.Mode),
				ModifiedTime: childDir.Modified.Unix(),
			})
		} else if fileEntry, exists := c.fileEntries[childPath]; exists {
			// It's a file
			name := getBaseName(childPath)
			entries = append(entries, &protocol.DirectoryEntry{
				Name:         name,
				Path:         childPath,
				IsDirectory:  false,
				Size:         fileEntry.Size,
				Mode:         uint32(fileEntry.Mode),
				ModifiedTime: fileEntry.Modified.Unix(),
			})
		}
	}

	return &protocol.ListDirectoryResponse{
		Success: true,
		Entries: entries,
	}, nil
}

// DeleteDirectory removes a directory
func (c *Coordinator) DeleteDirectory(ctx context.Context, req *protocol.DeleteDirectoryRequest) (*protocol.DeleteDirectoryResponse, error) {
	c.logger.Info("RemoveDirectory request", zap.String("path", req.Path))

	c.directoryMutex.Lock()
	defer c.directoryMutex.Unlock()

	// Check if directory exists
	dir, exists := c.directories[req.Path]
	if !exists {
		return &protocol.DeleteDirectoryResponse{
			Success: false,
			Message: "Directory does not exist",
		}, nil
	}

	// Check if directory is empty (unless recursive)
	if !req.Recursive && len(dir.Children) > 0 {
		return &protocol.DeleteDirectoryResponse{
			Success: false,
			Message: "Directory is not empty",
		}, nil
	}

	// If recursive, remove all children first
	if req.Recursive {
		if err := c.removeDirectoryRecursive(req.Path); err != nil {
			return &protocol.DeleteDirectoryResponse{
				Success: false,
				Message: fmt.Sprintf("Failed to remove directory: %v", err),
			}, nil
		}
	}

	// Remove from parent's children list
	if dir.Parent != "" {
		if parentDir, exists := c.directories[dir.Parent]; exists {
			children := parentDir.Children
			for i, child := range children {
				if child == req.Path {
					parentDir.Children = append(children[:i], children[i+1:]...)
					break
				}
			}
		}
	}

	// Remove the directory
	delete(c.directories, req.Path)

	c.logger.Info("Directory removed successfully", zap.String("path", req.Path))

	return &protocol.DeleteDirectoryResponse{
		Success: true,
		Message: "Directory removed successfully",
	}, nil
}

// MoveEntry moves a directory or file to a new location
func (c *Coordinator) MoveEntry(ctx context.Context, req *protocol.MoveEntryRequest) (*protocol.MoveEntryResponse, error) {
	c.logger.Info("MoveDirectory request", zap.String("source", req.OldPath), zap.String("dest", req.NewPath))

	c.directoryMutex.Lock()
	defer c.directoryMutex.Unlock()

	// Check if source exists
	sourceDir, exists := c.directories[req.OldPath]
	if !exists {
		// Check if it's a file instead
		if _, fileExists := c.fileEntries[req.OldPath]; fileExists {
			// Handle file move
			return c.moveFile(req.OldPath, req.NewPath)
		}

		return &protocol.MoveEntryResponse{
			Success: false,
			Message: "Source path does not exist",
		}, nil
	}

	// Check if destination already exists
	if _, exists := c.directories[req.NewPath]; exists {
		return &protocol.MoveEntryResponse{
			Success: false,
			Message: "Destination already exists",
		}, nil
	}

	// Check if destination parent exists
	destParent := getParentPath(req.NewPath)
	if destParent != "" && destParent != "/" {
		if _, exists := c.directories[destParent]; !exists {
			return &protocol.MoveEntryResponse{
				Success: false,
				Message: "Destination parent directory does not exist",
			}, nil
		}
	}

	// Remove from old parent's children
	if sourceDir.Parent != "" {
		if parentDir, exists := c.directories[sourceDir.Parent]; exists {
			children := parentDir.Children
			for i, child := range children {
				if child == req.OldPath {
					parentDir.Children = append(children[:i], children[i+1:]...)
					break
				}
			}
		}
	}

	// Create new directory entry
	newDir := &types.Directory{
		Path:     req.NewPath,
		Parent:   destParent,
		Children: []string{},
		Mode:     sourceDir.Mode,
		Modified: time.Now(),
		Owner:    sourceDir.Owner,
	}

	// Update children paths recursively
	c.updateChildPaths(req.OldPath, req.NewPath, sourceDir.Children)

	// Add to new parent's children
	if destParent != "" {
		if parentDir, exists := c.directories[destParent]; exists {
			parentDir.Children = append(parentDir.Children, req.NewPath)
		}
	}

	// Update the directory map
	c.directories[req.NewPath] = newDir
	delete(c.directories, req.OldPath)

	c.logger.Info("Directory moved successfully",
		zap.String("source", req.OldPath),
		zap.String("dest", req.NewPath))

	return &protocol.MoveEntryResponse{
		Success: true,
		Message: "Directory moved successfully",
	}, nil
}

// Helper function to move a file (called from MoveEntry when source is a file)
func (c *Coordinator) moveFile(sourcePath, destPath string) (*protocol.MoveEntryResponse, error) {
	fileEntry, exists := c.fileEntries[sourcePath]
	if !exists {
		return &protocol.MoveEntryResponse{
			Success: false,
			Message: "Source file does not exist",
		}, nil
	}

	// Check if destination already exists
	if _, exists := c.fileEntries[destPath]; exists {
		return &protocol.MoveEntryResponse{
			Success: false,
			Message: "Destination file already exists",
		}, nil
	}

	// Check destination parent
	destParent := getParentPath(destPath)
	if destParent != "" && destParent != "/" {
		if destDir, exists := c.directories[destParent]; exists {
			// Add to new parent
			destDir.Children = append(destDir.Children, destPath)
		} else {
			return &protocol.MoveEntryResponse{
				Success: false,
				Message: "Destination parent directory does not exist",
			}, nil
		}
	}

	// Remove from old parent
	sourceParent := getParentPath(sourcePath)
	if sourceParent != "" {
		if parentDir, exists := c.directories[sourceParent]; exists {
			children := parentDir.Children
			for i, child := range children {
				if child == sourcePath {
					parentDir.Children = append(children[:i], children[i+1:]...)
					break
				}
			}
		}
	}

	// Update file entry
	newFileEntry := *fileEntry
	newFileEntry.Path = destPath
	newFileEntry.Modified = time.Now()

	// Update the maps
	c.fileEntries[destPath] = &newFileEntry
	delete(c.fileEntries, sourcePath)

	// Update chunk mapping
	c.chunkMutex.Lock()
	if chunks, exists := c.fileChunks[sourcePath]; exists {
		c.fileChunks[destPath] = chunks
		delete(c.fileChunks, sourcePath)
	}
	c.chunkMutex.Unlock()

	return &protocol.MoveEntryResponse{
		Success: true,
		Message: "File moved successfully",
	}, nil
}

// Helper function to recursively remove a directory
func (c *Coordinator) removeDirectoryRecursive(path string) error {
	dir, exists := c.directories[path]
	if !exists {
		return nil
	}

	// Remove all children
	for _, childPath := range dir.Children {
		if _, isDir := c.directories[childPath]; isDir {
			if err := c.removeDirectoryRecursive(childPath); err != nil {
				return err
			}
		} else if _, isFile := c.fileEntries[childPath]; isFile {
			// Remove file
			delete(c.fileEntries, childPath)

			// Remove chunk mappings
			c.chunkMutex.Lock()
			delete(c.fileChunks, childPath)
			c.chunkMutex.Unlock()
		}
	}

	return nil
}

// Helper function to recursively update child paths after move
func (c *Coordinator) updateChildPaths(oldBase, newBase string, children []string) {
	for _, childPath := range children {
		newChildPath := strings.Replace(childPath, oldBase, newBase, 1)

		if childDir, exists := c.directories[childPath]; exists {
			// Update directory
			childDir.Path = newChildPath
			childDir.Parent = getParentPath(newChildPath)
			c.directories[newChildPath] = childDir
			delete(c.directories, childPath)

			// Recursively update its children
			c.updateChildPaths(childPath, newChildPath, childDir.Children)
		} else if childFile, exists := c.fileEntries[childPath]; exists {
			// Update file
			childFile.Path = newChildPath
			c.fileEntries[newChildPath] = childFile
			delete(c.fileEntries, childPath)

			// Update chunk mapping
			c.chunkMutex.Lock()
			if chunks, exists := c.fileChunks[childPath]; exists {
				c.fileChunks[newChildPath] = chunks
				delete(c.fileChunks, childPath)
			}
			c.chunkMutex.Unlock()
		}
	}
}

// StatEntry returns information about a file or directory
func (c *Coordinator) StatEntry(ctx context.Context, req *protocol.StatEntryRequest) (*protocol.StatEntryResponse, error) {
	c.logger.Debug("StatEntry request", zap.String("path", req.Path))

	c.directoryMutex.RLock()
	defer c.directoryMutex.RUnlock()

	// Check if it's a directory
	if dir, exists := c.directories[req.Path]; exists {
		return &protocol.StatEntryResponse{
			Success: true,
			Entry: &protocol.DirectoryEntry{
				Name:         getBaseName(req.Path),
				Path:         req.Path,
				IsDirectory:  true,
				Size:         0,
				Mode:         uint32(dir.Mode),
				ModifiedTime: dir.Modified.Unix(),
			},
		}, nil
	}

	// Check if it's a file
	if fileEntry, exists := c.fileEntries[req.Path]; exists {
		return &protocol.StatEntryResponse{
			Success: true,
			Entry: &protocol.DirectoryEntry{
				Name:         getBaseName(req.Path),
				Path:         req.Path,
				IsDirectory:  false,
				Size:         fileEntry.Size,
				Mode:         uint32(fileEntry.Mode),
				ModifiedTime: fileEntry.Modified.Unix(),
			},
		}, nil
	}

	return &protocol.StatEntryResponse{
		Success: false,
		Entry:   nil,
	}, nil
}

// Helper function to get parent path
func getParentPath(path string) string {
	if path == "/" || path == "" {
		return ""
	}

	lastSlash := strings.LastIndex(path, "/")
	if lastSlash == 0 {
		return "/"
	}
	if lastSlash == -1 {
		return ""
	}

	return path[:lastSlash]
}

// Helper function to get base name from path
func getBaseName(path string) string {
	if path == "/" {
		return "/"
	}

	parts := strings.Split(path, "/")
	if len(parts) > 0 {
		return parts[len(parts)-1]
	}

	return path
}
