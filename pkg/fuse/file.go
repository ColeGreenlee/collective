package fuse

import (
	"context"
	"syscall"
	"time"

	"collective/pkg/protocol"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
	"go.uber.org/zap"
	"google.golang.org/grpc"
)

// CollectiveFile represents a file in the collective storage
type CollectiveFile struct {
	fs.Inode
	coordinatorAddr string
	logger          *zap.Logger
	path            string
	size            int64
}

// NewCollectiveFile creates a new file instance
func NewCollectiveFile(path string, coordinatorAddr string, logger *zap.Logger) *CollectiveFile {
	return &CollectiveFile{
		path:            path,
		coordinatorAddr: coordinatorAddr,
		logger:          logger,
	}
}

// Getattr returns file attributes
func (f *CollectiveFile) Getattr(ctx context.Context, fh fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	// For now, return basic file attributes
	out.Attr.Mode = 0644 | syscall.S_IFREG
	out.Attr.Size = uint64(f.size)
	out.Attr.Nlink = 1

	// Use current time as placeholder
	now := uint64(time.Now().Unix())
	out.Attr.Mtime = now
	out.Attr.Atime = now
	out.Attr.Ctime = now

	// Cache for a short time
	out.SetTimeout(1 * time.Second)

	return 0
}

// Open opens a file for reading/writing
func (f *CollectiveFile) Open(ctx context.Context, flags uint32) (fh fs.FileHandle, fuseFlags uint32, errno syscall.Errno) {
	// For now, just return a basic file handle
	// TODO: Implement actual file opening with coordinator
	return nil, fuse.FOPEN_KEEP_CACHE, 0
}

// Read reads data from the file
func (f *CollectiveFile) Read(ctx context.Context, fh fs.FileHandle, dest []byte, off int64) (fuse.ReadResult, syscall.Errno) {
	f.logger.Debug("Read request", zap.String("path", f.path), zap.Int64("offset", off), zap.Int("size", len(dest)))

	// Connect to coordinator
	conn, err := grpc.Dial(f.coordinatorAddr, grpc.WithInsecure())
	if err != nil {
		f.logger.Error("Failed to connect to coordinator", zap.Error(err))
		return nil, syscall.EIO
	}
	defer conn.Close()

	client := protocol.NewCoordinatorClient(conn)
	readCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	// Request data from coordinator
	resp, err := client.ReadFile(readCtx, &protocol.ReadFileRequest{
		Path:   f.path,
		Offset: off,
		Length: int64(len(dest)),
	})

	if err != nil {
		f.logger.Error("Failed to read file from coordinator", zap.Error(err))
		return nil, syscall.EIO
	}

	if !resp.Success {
		return nil, syscall.ENOENT
	}

	return fuse.ReadResultData(resp.Data), 0
}

// Write writes data to the file
func (f *CollectiveFile) Write(ctx context.Context, fh fs.FileHandle, data []byte, off int64) (uint32, syscall.Errno) {
	f.logger.Debug("Write request", zap.String("path", f.path), zap.Int64("offset", off), zap.Int("size", len(data)))

	// Connect to coordinator
	conn, err := grpc.Dial(f.coordinatorAddr, grpc.WithInsecure())
	if err != nil {
		f.logger.Error("Failed to connect to coordinator", zap.Error(err))
		return 0, syscall.EIO
	}
	defer conn.Close()

	client := protocol.NewCoordinatorClient(conn)
	writeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	// Send data to coordinator
	resp, err := client.WriteFile(writeCtx, &protocol.WriteFileRequest{
		Path:   f.path,
		Data:   data,
		Offset: off,
	})

	if err != nil {
		f.logger.Error("Failed to write file to coordinator", zap.Error(err))
		return 0, syscall.EIO
	}

	if !resp.Success {
		f.logger.Error("Coordinator rejected write", zap.String("message", resp.Message))
		return 0, syscall.EIO
	}

	// Update file size
	f.size = off + int64(len(data))

	return uint32(resp.BytesWritten), 0
}

// Flush flushes cached data
func (f *CollectiveFile) Flush(ctx context.Context, fh fs.FileHandle) syscall.Errno {
	// TODO: Implement flushing data to coordinator
	f.logger.Debug("Flush request", zap.String("path", f.path))
	return 0
}

// Release releases the file handle
func (f *CollectiveFile) Release(ctx context.Context, fh fs.FileHandle) syscall.Errno {
	f.logger.Debug("Release request", zap.String("path", f.path))
	return 0
}
