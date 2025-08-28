# FUSE Filesystem Documentation

The collective storage includes a complete FUSE (Filesystem in Userspace) implementation for mounting distributed storage as a local filesystem.

## Usage

The FUSE implementation is fully functional with complete persistence to distributed storage:

```bash
# Mount the filesystem
./bin/collective mount /mnt/collective --coordinator localhost:8001 &

# All standard filesystem operations work
echo "Hello World" > /mnt/collective/test.txt           # Create files
mkdir /mnt/collective/documents                         # Create directories  
cp local_file.pdf /mnt/collective/documents/           # Copy files in
cat /mnt/collective/test.txt                           # Read files
rm /mnt/collective/old_file.txt                        # Delete files
ls -la /mnt/collective/                                # List contents
df -h /mnt/collective                                  # Check disk usage

# Files are automatically persisted to the distributed storage
# and visible through CLI:
./bin/collective ls / --coordinator localhost:8001

# Unmount when done
fusermount -u /mnt/collective
```

## Architecture

### Components

1. **CollectiveFS** (`pkg/fuse/filesystem.go`): Main filesystem with directory operations, inode management, and coordinator communication
2. **CollectiveFile** (`pkg/fuse/file.go`): File-specific operations, read/write, attributes, and handles  
3. **Protocol Extensions** (`proto/coordinator.proto`): gRPC operations - `CreateFile`, `ReadFile`, `WriteFile`, `DeleteFile`
4. **Coordinator Integration** (`pkg/coordinator/coordinator.go`): File metadata storage with directory structure

### Data Flow

```
FUSE Operation → gRPC Request → Coordinator → Metadata Storage
     ↓              ↓              ↓              ↓
   Create()    → CreateFile()  → File Entry   → Parent/Child Links
   Write()     → WriteFile()   → Size/Time    → Chunk Storage  
   Read()      → ReadFile()    → Retrieve     → Chunk Retrieval
   Readdir()   → ListDirectory() → Children   → Files + Directories
```

### Metadata Storage Structure

```go
// In coordinator memory
directories map[string]*types.Directory  // Path-based directory metadata
fileEntries map[string]*types.FileEntry  // Path-based file metadata

// Directory metadata
type Directory struct {
    Path     string     // Full path: "/documents/photos"  
    Parent   string     // Parent path: "/documents"
    Children []string   // Child paths (dirs + files)
    Mode     os.FileMode // POSIX permissions
    Modified time.Time   // Last modified timestamp
    Owner    MemberID    // Owning collective member
}

// File metadata  
type FileEntry struct {
    Path     string        // Full path: "/documents/resume.pdf"
    Size     int64         // File size in bytes
    Mode     os.FileMode   // POSIX permissions  
    Modified time.Time     // Last modified timestamp
    ChunkIDs []ChunkID     // Chunk storage integration
    Owner    MemberID      // Owning collective member
}
```

## Federation Integration

The FUSE filesystem fully supports federation features:

### Cross-Member Access
```bash
# Mount with federation-aware paths
./bin/collective mount /mnt/collective --coordinator alice-coordinator:8001

# Access files across the federation
ls /mnt/collective/@bob/shared/      # Files from Bob's domain
cp file.txt /mnt/collective/@carol/  # Copy to Carol's datastore
```

### DataStore Permissions
- Files respect DataStore permissions set through federation
- Cross-member reads/writes validated against permission grants
- Wildcard permissions (`*@*.collective.local`) honored

### Smart Caching
- Media files cached locally for streaming performance
- LRU eviction with configurable cache sizes
- Read-ahead and prefetching for sequential access
- Cache statistics available via metrics endpoint

## Platform Support

| Platform | FUSE Mounting | CLI Operations | Status |
|----------|---------------|----------------|--------|
| **Linux** | ✅ Full Support | ✅ All Operations | Native FUSE |
| **macOS** | ✅ Full Support | ✅ All Operations | Native FUSE |  
| **Windows** | ❌ Not Supported | ✅ All Operations | CLI Only (WinFsp planned) |

## Implementation Status

| Feature | Status | Details |
|---------|--------|---------|
| **Mount/Unmount** | ✅ Working | Standard FUSE mounting on Linux/macOS |
| **File Operations** | ✅ Working | Create, read, write, delete with full persistence |
| **Directory Operations** | ✅ Working | mkdir, rmdir, ls with nested directory support |
| **File Copy** | ✅ Working | cp works seamlessly between local and collective |
| **Filesystem Stats** | ✅ Working | df shows 4TB capacity with usage information |
| **Persistence** | ✅ Working | All operations persist to distributed storage |
| **Performance** | ✅ Optimized | Connection pooling and caching implemented |
| **Cross-platform** | ⚠️ Partial | Linux/macOS supported, Windows planned |

## Error Handling

| FUSE Error | Meaning | Common Causes |
|------------|---------|---------------|
| `ENOENT` | File/directory not found | Invalid path, deleted file |
| `EEXIST` | Already exists | Duplicate create operation |
| `EIO` | I/O error | Coordinator unavailable |
| `EPERM` | Permission denied | Future: Access control |

## Troubleshooting

### Mount fails on Linux

- Install FUSE: `sudo apt-get install fuse` (Ubuntu/Debian) or `sudo yum install fuse` (RHEL/CentOS)
- Add user to fuse group: `sudo usermod -a -G fuse $USER`

### Mount fails on macOS

- Install macFUSE: `brew install --cask macfuse`
- Enable kernel extension in System Preferences → Security & Privacy

### Windows mounting

- Currently not supported natively
- Use CLI directory operations: `mkdir`, `ls`, `rm`, `mv`
- WinFsp integration planned for future release