package fuse

import (
	"context"
	"os"
	"sync"
	"syscall"
	"time"

	"collective/pkg/protocol"
	"collective/pkg/types"

	"github.com/hanwen/go-fuse/v2/fuse"
	"github.com/hanwen/go-fuse/v2/fs"
	"go.uber.org/zap"
	"google.golang.org/grpc"
)

// CollectiveFS implements a FUSE filesystem for the collective storage
type CollectiveFS struct {
	fs.Inode
	coordinatorAddr string
	logger         *zap.Logger
	cache          *Cache
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

// NewCollectiveFS creates a new FUSE filesystem instance
func NewCollectiveFS(coordinatorAddr string, logger *zap.Logger) *CollectiveFS {
	return &CollectiveFS{
		coordinatorAddr: coordinatorAddr,
		logger:         logger,
		cache: &Cache{
			metadata:    make(map[string]*CachedEntry),
			metadataTTL: 5 * time.Second,
		},
	}
}

// OnAdd is called when this node is added to the file system
func (fs *CollectiveFS) OnAdd(ctx context.Context) {
	fs.logger.Info("FUSE filesystem mounted")
}

// Getattr returns file attributes for the given path
func (fs *CollectiveFS) Getattr(ctx context.Context, fh fuse.FileHandle, out *fuse.AttrOut) syscall.Errno {
	path := fs.getPath()
	
	entry, err := fs.statEntry(path)
	if err != nil {
		fs.logger.Error("Failed to stat entry", zap.String("path", path), zap.Error(err))
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
	
	return fuse.OK
}

// Readdir returns directory contents
func (fs *CollectiveFS) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	path := fs.getPath()
	
	entries, err := fs.listDirectory(path)
	if err != nil {
		fs.logger.Error("Failed to list directory", zap.String("path", path), zap.Error(err))
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
	
	return fs.NewListDirStream(fuseEntries), fuse.OK
}

// Lookup looks up a child node by name
func (fs *CollectiveFS) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	path := fs.getPath()
	childPath := path
	if path == "/" {
		childPath = "/" + name
	} else {
		childPath = path + "/" + name
	}
	
	entry, err := fs.statEntry(childPath)
	if err != nil {
		fs.logger.Debug("Lookup failed", zap.String("path", childPath), zap.Error(err))
		return nil, syscall.ENOENT
	}
	
	if entry == nil {
		return nil, syscall.ENOENT
	}
	
	// Create child inode
	var childNode fs.InodeEmbedder
	if entry.IsDirectory {
		childNode = &CollectiveFS{
			coordinatorAddr: fs.coordinatorAddr,
			logger:         fs.logger,
			cache:          fs.cache,
		}
	} else {
		childNode = &CollectiveFile{
			coordinatorAddr: fs.coordinatorAddr,
			logger:         fs.logger,
			path:           childPath,
			size:           entry.Size,
		}
	}
	
	child := fs.NewInode(ctx, childNode, fs.StableAttr{
		Mode: uint32(entry.Mode),
	})
	
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
	
	// Cache for a short time
	out.SetTimeout(1 * time.Second)
	
	return child, fuse.OK
}

// Create creates a new file
func (fs *CollectiveFS) Create(ctx context.Context, name string, flags uint32, mode uint32, out *fuse.EntryOut) (node *fs.Inode, fh fuse.FileHandle, fuseFlags uint32, errno syscall.Errno) {
	path := fs.getPath()
	childPath := path
	if path == "/" {
		childPath = "/" + name
	} else {
		childPath = path + "/" + name
	}
	
	fs.logger.Debug("Create file request", zap.String("path", childPath), zap.Uint32("mode", mode))
	
	// Connect to coordinator to create the file
	conn, err := grpc.Dial(fs.coordinatorAddr, grpc.WithInsecure())
	if err != nil {
		fs.logger.Error("Failed to connect to coordinator", zap.Error(err))
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
		fs.logger.Error("Failed to create file via coordinator", zap.Error(err))
		return nil, nil, 0, syscall.EIO
	}
	
	if !resp.Success {
		fs.logger.Error("Coordinator rejected file creation", zap.String("message", resp.Message))
		return nil, nil, 0, syscall.EEXIST
	}
	
	// Create file inode
	fileNode := &CollectiveFile{
		coordinatorAddr: fs.coordinatorAddr,
		logger:         fs.logger,
		path:           childPath,
		size:           0,
	}
	
	child := fs.NewInode(ctx, fileNode, fs.StableAttr{
		Mode: mode | syscall.S_IFREG,
	})
	
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
	
	// Cache for a short time
	out.SetTimeout(1 * time.Second)
	
	fs.logger.Info("Created file", zap.String("path", childPath))
	
	return child, nil, fuse.FOPEN_KEEP_CACHE, fuse.OK
}

// Mkdir creates a new directory
func (fs *CollectiveFS) Mkdir(ctx context.Context, name string, mode uint32, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	path := fs.getPath()
	childPath := path
	if path == "/" {
		childPath = "/" + name
	} else {
		childPath = path + "/" + name
	}
	
	err := fs.createDirectory(childPath, mode)
	if err != nil {
		fs.logger.Error("Failed to create directory", zap.String("path", childPath), zap.Error(err))
		return nil, syscall.EIO
	}
	
	// Create child inode for the new directory
	childNode := &CollectiveFS{
		coordinatorAddr: fs.coordinatorAddr,
		logger:         fs.logger,
		cache:          fs.cache,
	}
	
	child := fs.NewInode(ctx, childNode, fs.StableAttr{
		Mode: mode | syscall.S_IFDIR,
	})
	
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
	
	return child, fuse.OK
}

// Rmdir removes a directory
func (fs *CollectiveFS) Rmdir(ctx context.Context, name string) syscall.Errno {
	path := fs.getPath()
	childPath := path
	if path == "/" {
		childPath = "/" + name
	} else {
		childPath = path + "/" + name
	}
	
	err := fs.deleteDirectory(childPath, false)
	if err != nil {
		fs.logger.Error("Failed to remove directory", zap.String("path", childPath), zap.Error(err))
		return syscall.EIO
	}
	
	return fuse.OK
}

// Unlink removes a file
func (fs *CollectiveFS) Unlink(ctx context.Context, name string) syscall.Errno {
	path := fs.getPath()
	childPath := path
	if path == "/" {
		childPath = "/" + name
	} else {
		childPath = path + "/" + name
	}
	
	fs.logger.Debug("Unlink file request", zap.String("path", childPath))
	
	// Connect to coordinator to delete the file
	conn, err := grpc.Dial(fs.coordinatorAddr, grpc.WithInsecure())
	if err != nil {
		fs.logger.Error("Failed to connect to coordinator", zap.Error(err))
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
		fs.logger.Error("Failed to delete file via coordinator", zap.Error(err))
		return syscall.EIO
	}
	
	if !resp.Success {
		fs.logger.Error("Coordinator rejected file deletion", zap.String("message", resp.Message))
		return syscall.ENOENT
	}
	
	fs.logger.Info("Deleted file", zap.String("path", childPath))
	
	return fuse.OK
}

// Helper methods

// getPath returns the full path for this inode
func (fs *CollectiveFS) getPath() string {
	return fs.Path(fs.Root())
}

// statEntry calls the coordinator to get entry information
func (fs *CollectiveFS) statEntry(path string) (*protocol.DirectoryEntry, error) {
	// Check cache first
	if cached := fs.getCachedEntry(path); cached != nil {
		return cached.Entry, nil
	}
	
	conn, err := grpc.Dial(fs.coordinatorAddr, grpc.WithInsecure())
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
	fs.cacheEntry(path, &CachedEntry{
		Entry:    resp.Entry,
		CachedAt: time.Now(),
	})
	
	return resp.Entry, nil
}

// listDirectory calls the coordinator to list directory contents
func (fs *CollectiveFS) listDirectory(path string) ([]*protocol.DirectoryEntry, error) {
	// Check cache first
	if cached := fs.getCachedEntry(path); cached != nil && cached.Children != nil {
		return cached.Children, nil
	}
	
	conn, err := grpc.Dial(fs.coordinatorAddr, grpc.WithInsecure())
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
	fs.cacheEntry(path, &CachedEntry{
		Children: resp.Entries,
		CachedAt: time.Now(),
	})
	
	return resp.Entries, nil
}

// createDirectory calls the coordinator to create a directory
func (fs *CollectiveFS) createDirectory(path string, mode uint32) error {
	conn, err := grpc.Dial(fs.coordinatorAddr, grpc.WithInsecure())
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
	fs.invalidateCache(getParentPath(path))
	
	return nil
}

// deleteDirectory calls the coordinator to delete a directory
func (fs *CollectiveFS) deleteDirectory(path string, recursive bool) error {
	conn, err := grpc.Dial(fs.coordinatorAddr, grpc.WithInsecure())
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
	fs.invalidateCache(path)
	fs.invalidateCache(getParentPath(path))
	
	return nil
}

// Cache management methods

func (fs *CollectiveFS) getCachedEntry(path string) *CachedEntry {
	fs.cache.mu.RLock()
	defer fs.cache.mu.RUnlock()
	
	entry, exists := fs.cache.metadata[path]
	if !exists {
		return nil
	}
	
	// Check if entry is stale
	if time.Since(entry.CachedAt) > fs.cache.metadataTTL || entry.IsStale {
		return nil
	}
	
	return entry
}

func (fs *CollectiveFS) cacheEntry(path string, entry *CachedEntry) {
	fs.cache.mu.Lock()
	defer fs.cache.mu.Unlock()
	
	fs.cache.metadata[path] = entry
}

func (fs *CollectiveFS) invalidateCache(path string) {
	fs.cache.mu.Lock()
	defer fs.cache.mu.Unlock()
	
	if entry, exists := fs.cache.metadata[path]; exists {
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