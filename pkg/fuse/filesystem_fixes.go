package fuse

import (
	"context"
	"os"
	"syscall"
	"time"
	
	"collective/pkg/protocol"
	
	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
	"go.uber.org/zap"
	"google.golang.org/grpc"
)

// Statfs implements the filesystem statistics operation
// This is required for df and other tools to work
func (cfs *CollectiveFS) Statfs(ctx context.Context, out *fuse.StatfsOut) syscall.Errno {
	cfs.logger.Debug("Statfs called")
	
	// Set filesystem statistics
	// These values are somewhat arbitrary for a distributed filesystem
	const blockSize = 4096
	const totalBlocks = 1024 * 1024 * 1024 // ~4TB total space
	const freeBlocks = 768 * 1024 * 1024   // ~3TB free
	const availBlocks = freeBlocks
	
	out.Blocks = totalBlocks
	out.Bfree = freeBlocks
	out.Bavail = availBlocks
	out.Bsize = blockSize
	
	// Inode statistics (also arbitrary for distributed fs)
	out.Files = 1000000    // Total inodes
	out.Ffree = 999000     // Free inodes
	
	// Maximum filename length
	out.NameLen = 255
	
	// Fragment size (same as block size)
	out.Frsize = blockSize
	
	return 0
}

// Fsync flushes any buffered data for a file
func (cfs *CollectiveFS) Fsync(ctx context.Context, fh fs.FileHandle, flags uint32) syscall.Errno {
	cfs.logger.Debug("Fsync called", zap.String("path", cfs.getPath()))
	// For now, we don't have any local buffers to flush
	// All writes go directly to the coordinator
	return 0
}

// Flush is called on file close
func (cfs *CollectiveFS) Flush(ctx context.Context, fh fs.FileHandle) syscall.Errno {
	cfs.logger.Debug("Flush called", zap.String("path", cfs.getPath()))
	// Similar to Fsync, we don't buffer locally
	return 0
}

// Access checks if the calling process can access the file
func (cfs *CollectiveFS) Access(ctx context.Context, mask uint32) syscall.Errno {
	path := cfs.getPath()
	cfs.logger.Debug("Access called", zap.String("path", path), zap.Uint32("mask", mask))
	
	// Check if the file exists
	entry, err := cfs.statEntry(path)
	if err != nil {
		return syscall.ENOENT
	}
	
	// For now, allow all access if the file exists
	// In the future, implement proper permission checking
	if entry == nil {
		return syscall.ENOENT
	}
	
	return 0
}

// Rename moves/renames a file or directory
func (cfs *CollectiveFS) Rename(ctx context.Context, name string, newParent fs.InodeEmbedder, newName string, flags uint32) syscall.Errno {
	oldPath := cfs.getPath() + "/" + name
	
	// Get the new parent's path
	var newParentPath string
	if newParentFS, ok := newParent.(*CollectiveFS); ok {
		newParentPath = newParentFS.getPath()
	} else {
		return syscall.EIO
	}
	
	newPath := newParentPath + "/" + newName
	
	cfs.logger.Debug("Rename called", 
		zap.String("oldPath", oldPath),
		zap.String("newPath", newPath))
	
	// Use the existing move functionality via gRPC
	conn, err := grpc.Dial(cfs.coordinatorAddr, grpc.WithInsecure())
	if err != nil {
		cfs.logger.Error("Failed to connect to coordinator", zap.Error(err))
		return syscall.EIO
	}
	defer conn.Close()
	
	client := protocol.NewCoordinatorClient(conn)
	
	// Call MoveEntry RPC
	_, err = client.MoveEntry(ctx, &protocol.MoveEntryRequest{
		OldPath: oldPath,
		NewPath: newPath,
	})
	
	if err != nil {
		cfs.logger.Error("Failed to rename entry", zap.Error(err))
		return syscall.EIO
	}
	
	// Invalidate cache for both paths
	cfs.invalidateCache(oldPath)
	cfs.invalidateCache(newPath)
	cfs.invalidateCache(cfs.getPath())
	cfs.invalidateCache(newParentPath)
	
	return 0
}

// Setattr sets file attributes
func (cfs *CollectiveFS) Setattr(ctx context.Context, fh fs.FileHandle, in *fuse.SetAttrIn, out *fuse.AttrOut) syscall.Errno {
	path := cfs.getPath()
	cfs.logger.Debug("Setattr called", zap.String("path", path))
	
	// For now, just update the cached attributes
	// In the future, implement actual attribute changes
	entry, err := cfs.statEntry(path)
	if err != nil {
		return syscall.ENOENT
	}
	
	if entry == nil {
		return syscall.ENOENT
	}
	
	// Fill out with current attributes
	out.Size = uint64(entry.Size)
	out.Mode = entry.Mode
	out.Mtime = uint64(entry.ModifiedTime)
	out.Mtimensec = 0
	out.Atime = out.Mtime
	out.Atimensec = 0
	out.Ctime = out.Mtime
	out.Ctimensec = 0
	
	if entry.IsDirectory {
		out.Mode |= syscall.S_IFDIR
	} else {
		out.Mode |= syscall.S_IFREG
	}
	
	// Set reasonable defaults
	out.Uid = uint32(os.Getuid())
	out.Gid = uint32(os.Getgid())
	out.Nlink = 1
	
	// Cache timeout
	out.SetTimeout(time.Second)
	
	return 0
}

// Open opens a file (not for creation)
func (cfs *CollectiveFS) Open(ctx context.Context, flags uint32) (fs.FileHandle, uint32, syscall.Errno) {
	path := cfs.getPath()
	cfs.logger.Debug("Open called", zap.String("path", path), zap.Uint32("flags", flags))
	
	// Check if file exists
	entry, err := cfs.statEntry(path)
	if err != nil || entry == nil {
		return nil, 0, syscall.ENOENT
	}
	
	// Create a file handle
	file := NewCollectiveFile(path, cfs.coordinatorAddr, cfs.logger)
	
	return file, fuse.FOPEN_DIRECT_IO, 0
}