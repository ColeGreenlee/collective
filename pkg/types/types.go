package types

import (
	"os"
	"time"
)

type MemberID string
type NodeID string
type ChunkID string
type FileID string

type Member struct {
	ID          MemberID
	Address     string
	Nodes       []NodeID
	LastSeen    time.Time
	IsConnected bool
}

type StorageNode struct {
	ID            NodeID
	MemberID      MemberID
	Address       string
	TotalCapacity int64
	UsedCapacity  int64
	IsHealthy     bool
	LastHealthCheck time.Time
}

type Chunk struct {
	ID           ChunkID
	FileID       FileID
	Index        int
	Size         int64 // Size of actual data (possibly compressed)
	OriginalSize int64 // Original uncompressed size
	Hash         string
	Data         []byte
	Compressed   bool // Whether data is compressed
}

type File struct {
	ID        FileID
	Name      string
	Size      int64
	Chunks    []ChunkLocation
	CreatedAt time.Time
	OwnerID   MemberID
}

type ChunkLocation struct {
	ChunkID ChunkID
	NodeID  NodeID
	Index   int
	Size    int64
}

type Directory struct {
	Path     string
	Parent   string
	Children []string
	Mode     os.FileMode
	Modified time.Time
	Owner    MemberID
}

type FileEntry struct {
	Path     string
	Size     int64
	Mode     os.FileMode
	Modified time.Time
	ChunkIDs []ChunkID
	Owner    MemberID
}