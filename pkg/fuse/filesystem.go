package fuse

import (
	"context"
	"fmt"
	"os"
	"sync"
	"syscall"
	"time"

	"collective/pkg/protocol"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
	"go.uber.org/zap"
	"google.golang.org/grpc"
)

// CollectiveFS implements a FUSE filesystem for the collective storage
type CollectiveFS struct {
	fs.Inode
	coordinatorAddr string
	logger         *zap.Logger
	cache          *Cache
	path           string // Track current path for this inode
}

// Cache holds metadata and data caches for performance
type Cache struct {
	mu           sync.RWMutex
	metadata     map[string]*CachedEntry
	metadataTTL  time.Duration
}

// CachedEntry represents a cached directory entry
type CachedEntry struct {
	Entry      *protocol.DirectoryEntry
	Children   []*protocol.DirectoryEntry
	CachedAt   time.Time
	IsStale    bool
}

// ListDirStream implements fs.DirStream for directory listings
type ListDirStream struct {
	entries []fuse.DirEntry
	index   int
}

// NewListDirStream creates a new directory stream
func NewListDirStream(entries []fuse.DirEntry) *ListDirStream {
	return &ListDirStream{
		entries: entries,
		index:   0,
	}
}

// HasNext returns true if there are more entries
func (s *ListDirStream) HasNext() bool {
	return s.index < len(s.entries)
}

// Next returns the next entry
func (s *ListDirStream) Next() (fuse.DirEntry, syscall.Errno) {
	if !s.HasNext() {
		return fuse.DirEntry{}, syscall.EIO
	}
	entry := s.entries[s.index]
	s.index++
	return entry, 0
}

// Close closes the stream
func (s *ListDirStream) Close() {
	// Nothing to close
}

// NewCollectiveFS creates a new FUSE filesystem instance
func NewCollectiveFS(coordinatorAddr string, logger *zap.Logger) *CollectiveFS {
	return &CollectiveFS{
		coordinatorAddr: coordinatorAddr,
		logger:         logger,
		path:           "/",  // Root node starts at /
		cache: &Cache{
			metadata:    make(map[string]*CachedEntry),
			metadataTTL: 5 * time.Second,
		},
	}
}

// OnAdd is called when this node is added to the file system
func (cfs *CollectiveFS) OnAdd(ctx context.Context) {
	cfs.logger.Info("FUSE filesystem mounted")
}

// Getattr returns file attributes for the given path
func (cfs *CollectiveFS) Getattr(ctx context.Context, fh fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	path := cfs.getPath()
	
	entry, err := cfs.statEntry(path)
	if err != nil {
		cfs.logger.Error("Failed to stat entry", zap.String("path", path), zap.Error(err))
		return syscall.ENOENT
	}
	
	if entry == nil {
		return syscall.ENOENT
	}
	
	// Convert to FUSE attributes
	out.Attr.Mode = uint32(entry.Mode)
	out.Attr.Size = uint64(entry.Size)
	out.Attr.Mtime = uint64(entry.ModifiedTime)
	out.Attr.Atime = uint64(entry.ModifiedTime)
	out.Attr.Ctime = uint64(entry.ModifiedTime)
	
	if entry.IsDirectory {
		out.Attr.Mode = out.Attr.Mode | syscall.S_IFDIR
	} else {
		out.Attr.Mode = out.Attr.Mode | syscall.S_IFREG
	}
	
	// Set reasonable defaults
	out.Attr.Nlink = 1
	out.Attr.Uid = uint32(os.Getuid())
	out.Attr.Gid = uint32(os.Getgid())
	
	// Cache attributes for a short time
	out.SetTimeout(1 * time.Second)
	
	return 0
}

// Readdir returns directory contents
func (cfs *CollectiveFS) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	path := cfs.getPath()
	
	entries, err := cfs.listDirectory(path)
	if err != nil {
		cfs.logger.Error("Failed to list directory", zap.String("path", path), zap.Error(err))
		return nil, syscall.EIO
	}
	
	var fuseEntries []fuse.DirEntry
	
	// Add . and .. entries
	fuseEntries = append(fuseEntries, fuse.DirEntry{
		Mode: syscall.S_IFDIR,
		Name: ".",
	})
	
	if path != "/" {
		fuseEntries = append(fuseEntries, fuse.DirEntry{
			Mode: syscall.S_IFDIR,
			Name: "..",
		})
	}
	
	// Add actual directory entries
	for _, entry := range entries {
		mode := uint32(entry.Mode)
		if entry.IsDirectory {
			mode = mode | syscall.S_IFDIR
		} else {
			mode = mode | syscall.S_IFREG
		}
		
		fuseEntries = append(fuseEntries, fuse.DirEntry{
			Mode: mode,
			Name: entry.Name,
		})
	}
	
	return NewListDirStream(fuseEntries), 0
}

// Lookup looks up a child node by name
func (cfs *CollectiveFS) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	path := cfs.getPath()
	childPath := path
	if path == "/" {
		childPath = "/" + name
	} else {
		childPath = path + "/" + name
	}
	
	entry, err := cfs.statEntry(childPath)
	if err != nil {
		cfs.logger.Debug("Lookup failed", zap.String("path", childPath), zap.Error(err))
		return nil, syscall.ENOENT
	}
	
	if entry == nil {
		return nil, syscall.ENOENT
	}
	
	// Create child inode
	var child *fs.Inode
	if entry.IsDirectory {
		childNode := &CollectiveFS{
			coordinatorAddr: cfs.coordinatorAddr,
			logger:         cfs.logger,
			cache:          cfs.cache,
			path:           childPath,
		}
		stable := fs.StableAttr{
			Mode: fuse.S_IFDIR,
			Ino:  0, // Let FUSE assign inode numbers
		}
		child = cfs.NewInode(ctx, childNode, stable)
	} else {
		childNode := &CollectiveFile{
			coordinatorAddr: cfs.coordinatorAddr,
			logger:         cfs.logger,
			path:           childPath,
			size:           entry.Size,
		}
		stable := fs.StableAttr{
			Mode: fuse.S_IFREG,
			Ino:  0,
		}
		child = cfs.NewInode(ctx, childNode, stable)
	}
	
	// Set entry attributes
	out.Attr.Mode = uint32(entry.Mode)
	out.Attr.Size = uint64(entry.Size)
	out.Attr.Mtime = uint64(entry.ModifiedTime)
	out.Attr.Atime = uint64(entry.ModifiedTime)
	out.Attr.Ctime = uint64(entry.ModifiedTime)
	
	if entry.IsDirectory {
		out.Attr.Mode = out.Attr.Mode | syscall.S_IFDIR
	} else {
		out.Attr.Mode = out.Attr.Mode | syscall.S_IFREG
	}
	
	// Set reasonable defaults
	out.Attr.Nlink = 1
	out.Attr.Uid = uint32(os.Getuid())
	out.Attr.Gid = uint32(os.Getgid())
	
	// Set timeout for cache
	out.SetEntryTimeout(1 * time.Second)
	out.SetAttrTimeout(1 * time.Second)
	
	return child, 0
}

// Create creates a new file
func (cfs *CollectiveFS) Create(ctx context.Context, name string, flags uint32, mode uint32, out *fuse.EntryOut) (node *fs.Inode, fh fs.FileHandle, fuseFlags uint32, errno syscall.Errno) {
	path := cfs.getPath()
	childPath := path
	if path == "/" {
		childPath = "/" + name
	} else {
		childPath = path + "/" + name
	}
	
	cfs.logger.Debug("Create file request", zap.String("path", childPath), zap.Uint32("mode", mode))
	
	// Connect to coordinator to create the file
	conn, err := grpc.Dial(cfs.coordinatorAddr, grpc.WithInsecure())
	if err != nil {
		cfs.logger.Error("Failed to connect to coordinator", zap.Error(err))
		return nil, nil, 0, syscall.EIO
	}
	defer conn.Close()
	
	client := protocol.NewCoordinatorClient(conn)
	createCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	
	// Create the file
	resp, err := client.CreateFile(createCtx, &protocol.CreateFileRequest{
		Path: childPath,
		Mode: mode,
	})
	
	if err != nil {
		cfs.logger.Error("Failed to create file via coordinator", zap.Error(err))
		return nil, nil, 0, syscall.EIO
	}
	
	if !resp.Success {
		cfs.logger.Error("Coordinator rejected file creation", zap.String("message", resp.Message))
		return nil, nil, 0, syscall.EEXIST
	}
	
	// Create file inode
	fileNode := &CollectiveFile{
		coordinatorAddr: cfs.coordinatorAddr,
		logger:         cfs.logger,
		path:           childPath,
		size:           0,
	}
	
	stable := fs.StableAttr{
		Mode: fuse.S_IFREG,
		Ino:  0,
	}
	child := cfs.NewInode(ctx, fileNode, stable)
	
	// Set entry attributes
	out.Attr.Mode = mode | syscall.S_IFREG
	out.Attr.Size = 0
	now := uint64(time.Now().Unix())
	out.Attr.Mtime = now
	out.Attr.Atime = now
	out.Attr.Ctime = now
	
	// Set reasonable defaults
	out.Attr.Nlink = 1
	out.Attr.Uid = uint32(os.Getuid())
	out.Attr.Gid = uint32(os.Getgid())
	
	// Set timeout for cache
	out.SetEntryTimeout(1 * time.Second)
	out.SetAttrTimeout(1 * time.Second)
	
	cfs.logger.Info("Created file", zap.String("path", childPath))
	
	return child, nil, fuse.FOPEN_KEEP_CACHE, 0
}

// Mkdir creates a new directory
func (cfs *CollectiveFS) Mkdir(ctx context.Context, name string, mode uint32, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	path := cfs.getPath()
	childPath := path
	if path == "/" {
		childPath = "/" + name
	} else {
		childPath = path + "/" + name
	}
	
	err := cfs.createDirectory(childPath, mode)
	if err != nil {
		cfs.logger.Error("Failed to create directory", zap.String("path", childPath), zap.Error(err))
		return nil, syscall.EIO
	}
	
	// Create child inode for the new directory
	childNode := &CollectiveFS{
		coordinatorAddr: cfs.coordinatorAddr,
		logger:         cfs.logger,
		cache:          cfs.cache,
		path:           childPath,
	}
	
	stable := fs.StableAttr{
		Mode: fuse.S_IFDIR,
		Ino:  0,
	}
	child := cfs.NewInode(ctx, childNode, stable)
	
	// Set entry attributes
	out.Attr.Mode = mode | syscall.S_IFDIR
	out.Attr.Size = 0
	now := uint64(time.Now().Unix())
	out.Attr.Mtime = now
	out.Attr.Atime = now
	out.Attr.Ctime = now
	
	// Set reasonable defaults
	out.Attr.Nlink = 1
	out.Attr.Uid = uint32(os.Getuid())
	out.Attr.Gid = uint32(os.Getgid())
	
	// Set timeout for cache
	out.SetEntryTimeout(1 * time.Second)
	out.SetAttrTimeout(1 * time.Second)
	
	return child, 0
}

// Rmdir removes a directory
func (cfs *CollectiveFS) Rmdir(ctx context.Context, name string) syscall.Errno {
	path := cfs.getPath()
	childPath := path
	if path == "/" {
		childPath = "/" + name
	} else {
		childPath = path + "/" + name
	}
	
	err := cfs.deleteDirectory(childPath, false)
	if err != nil {
		cfs.logger.Error("Failed to remove directory", zap.String("path", childPath), zap.Error(err))
		return syscall.EIO
	}
	
	return 0
}

// Unlink removes a file
func (cfs *CollectiveFS) Unlink(ctx context.Context, name string) syscall.Errno {
	path := cfs.getPath()
	childPath := path
	if path == "/" {
		childPath = "/" + name
	} else {
		childPath = path + "/" + name
	}
	
	cfs.logger.Debug("Unlink file request", zap.String("path", childPath))
	
	// Connect to coordinator to delete the file
	conn, err := grpc.Dial(cfs.coordinatorAddr, grpc.WithInsecure())
	if err != nil {
		cfs.logger.Error("Failed to connect to coordinator", zap.Error(err))
		return syscall.EIO
	}
	defer conn.Close()
	
	client := protocol.NewCoordinatorClient(conn)
	deleteCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	
	// Delete the file
	resp, err := client.DeleteFile(deleteCtx, &protocol.DeleteFileRequest{
		Path: childPath,
	})
	
	if err != nil {
		cfs.logger.Error("Failed to delete file via coordinator", zap.Error(err))
		return syscall.EIO
	}
	
	if !resp.Success {
		cfs.logger.Error("Coordinator rejected file deletion", zap.String("message", resp.Message))
		return syscall.ENOENT
	}
	
	cfs.logger.Info("Deleted file", zap.String("path", childPath))
	
	return 0
}

// Helper methods

// getPath returns the full path for this inode
func (cfs *CollectiveFS) getPath() string {
	if cfs.path == "" {
		return "/"
	}
	return cfs.path
}

// statEntry calls the coordinator to get entry information
func (cfs *CollectiveFS) statEntry(path string) (*protocol.DirectoryEntry, error) {
	// Check cache first
	if cached := cfs.getCachedEntry(path); cached != nil {
		return cached.Entry, nil
	}
	
	conn, err := grpc.Dial(cfs.coordinatorAddr, grpc.WithInsecure())
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	
	client := protocol.NewCoordinatorClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	
	resp, err := client.StatEntry(ctx, &protocol.StatEntryRequest{
		Path: path,
	})
	if err != nil {
		return nil, err
	}
	
	if !resp.Success {
		return nil, nil
	}
	
	// Cache the result
	cfs.cacheEntry(path, &CachedEntry{
		Entry:    resp.Entry,
		CachedAt: time.Now(),
	})
	
	return resp.Entry, nil
}

// listDirectory calls the coordinator to list directory contents
func (cfs *CollectiveFS) listDirectory(path string) ([]*protocol.DirectoryEntry, error) {
	// Check cache first
	if cached := cfs.getCachedEntry(path); cached != nil && cached.Children != nil {
		return cached.Children, nil
	}
	
	conn, err := grpc.Dial(cfs.coordinatorAddr, grpc.WithInsecure())
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	
	client := protocol.NewCoordinatorClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	
	resp, err := client.ListDirectory(ctx, &protocol.ListDirectoryRequest{
		Path: path,
	})
	if err != nil {
		return nil, err
	}
	
	if !resp.Success {
		return nil, nil
	}
	
	// Cache the result
	cfs.cacheEntry(path, &CachedEntry{
		Children: resp.Entries,
		CachedAt: time.Now(),
	})
	
	return resp.Entries, nil
}

// createDirectory calls the coordinator to create a directory
func (cfs *CollectiveFS) createDirectory(path string, mode uint32) error {
	conn, err := grpc.Dial(cfs.coordinatorAddr, grpc.WithInsecure())
	if err != nil {
		return err
	}
	defer conn.Close()
	
	client := protocol.NewCoordinatorClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	
	resp, err := client.CreateDirectory(ctx, &protocol.CreateDirectoryRequest{
		Path: path,
		Mode: mode,
	})
	if err != nil {
		return err
	}
	
	if !resp.Success {
		return syscall.Errno(syscall.EIO)
	}
	
	// Invalidate cache for parent directory
	cfs.invalidateCache(getParentPath(path))
	
	return nil
}

// deleteDirectory calls the coordinator to delete a directory
func (cfs *CollectiveFS) deleteDirectory(path string, recursive bool) error {
	conn, err := grpc.Dial(cfs.coordinatorAddr, grpc.WithInsecure())
	if err != nil {
		return err
	}
	defer conn.Close()
	
	client := protocol.NewCoordinatorClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	
	resp, err := client.DeleteDirectory(ctx, &protocol.DeleteDirectoryRequest{
		Path:      path,
		Recursive: recursive,
	})
	if err != nil {
		return err
	}
	
	if !resp.Success {
		return syscall.Errno(syscall.EIO)
	}
	
	// Invalidate cache
	cfs.invalidateCache(path)
	cfs.invalidateCache(getParentPath(path))
	
	return nil
}

// Cache management methods

func (cfs *CollectiveFS) getCachedEntry(path string) *CachedEntry {
	cfs.cache.mu.RLock()
	defer cfs.cache.mu.RUnlock()
	
	entry, exists := cfs.cache.metadata[path]
	if !exists {
		return nil
	}
	
	// Check if entry is stale
	if time.Since(entry.CachedAt) > cfs.cache.metadataTTL || entry.IsStale {
		return nil
	}
	
	return entry
}

func (cfs *CollectiveFS) cacheEntry(path string, entry *CachedEntry) {
	cfs.cache.mu.Lock()
	defer cfs.cache.mu.Unlock()
	
	cfs.cache.metadata[path] = entry
}

func (cfs *CollectiveFS) invalidateCache(path string) {
	cfs.cache.mu.Lock()
	defer cfs.cache.mu.Unlock()
	
	if entry, exists := cfs.cache.metadata[path]; exists {
		entry.IsStale = true
	}
}

// Helper function to get parent path
func getParentPath(path string) string {
	if path == "/" || path == "" {
		return ""
	}
	
	// Remove trailing slash if present
	if path[len(path)-1] == '/' {
		path = path[:len(path)-1]
	}
	
	lastSlash := len(path) - 1
	for lastSlash >= 0 && path[lastSlash] != '/' {
		lastSlash--
	}
	
	if lastSlash <= 0 {
		return "/"
	}
	return path[:lastSlash]
}

// Mount mounts the CollectiveFS at the specified mountpoint
func Mount(coordinatorAddr string, mountpoint string, logger *zap.Logger) error {
	// Create the filesystem
	collectiveFS := NewCollectiveFS(coordinatorAddr, logger)
	
	// Create mount options with debug to see operations
	opts := &fs.Options{
		MountOptions: fuse.MountOptions{
			Debug:      false,  // Disable debug for cleaner output
			AllowOther: false,
			Name:       "collective",
			FsName:     fmt.Sprintf("collective@%s", coordinatorAddr),
			DirectMount: true,
		},
	}
	
	// Create and start the filesystem server
	server, err := fs.Mount(mountpoint, collectiveFS, opts)
	if err != nil {
		return fmt.Errorf("failed to mount filesystem: %w", err)
	}
	
	logger.Info("Filesystem mounted successfully",
		zap.String("mountpoint", mountpoint),
		zap.String("coordinator", coordinatorAddr))
	
	// Wait for unmount
	server.Wait()
	
	return nil
}