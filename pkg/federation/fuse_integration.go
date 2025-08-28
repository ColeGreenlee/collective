package federation

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"collective/pkg/types"
	"go.uber.org/zap"
)

// FederatedFUSEAdapter provides FUSE filesystem access to federated storage
type FederatedFUSEAdapter struct {
	localDomain   string
	connManager   *ConnectionManager
	permManager   *PermissionManager
	placement     *PlacementController
	cache         *MediaCache
	logger        *zap.Logger
	
	// Performance optimizations
	prefetchQueue chan PrefetchRequest
	readAhead     bool
	cacheSize     int64
	
	mu sync.RWMutex
}

// MediaCache provides intelligent caching for media streaming
type MediaCache struct {
	mu          sync.RWMutex
	entries     map[string]*CacheEntry
	lru         *LRUList
	maxSize     int64
	currentSize int64
	hitRate     float64
	missRate    float64
	
	// Metrics
	hits   int64
	misses int64
}

// CacheEntry represents a cached chunk
type CacheEntry struct {
	ChunkID      string
	Data         []byte
	Size         int64
	LastAccessed time.Time
	AccessCount  int
	Priority     CachePriority
	node         *LRUNode
}

// CachePriority determines eviction order
type CachePriority int

const (
	PriorityLow CachePriority = iota
	PriorityNormal
	PriorityHigh    // Currently streaming
	PriorityPinned  // Frequently accessed
)

// LRUList implements an LRU eviction policy
type LRUList struct {
	head *LRUNode
	tail *LRUNode
}

// LRUNode is a node in the LRU list
type LRUNode struct {
	entry *CacheEntry
	prev  *LRUNode
	next  *LRUNode
}

// PrefetchRequest represents a chunk to prefetch
type PrefetchRequest struct {
	ChunkID  string
	Priority int
	Deadline time.Time
}

// NewFederatedFUSEAdapter creates a new FUSE adapter for federation
func NewFederatedFUSEAdapter(
	localDomain string,
	connManager *ConnectionManager,
	permManager *PermissionManager,
	placement *PlacementController,
	logger *zap.Logger,
) *FederatedFUSEAdapter {
	if logger == nil {
		logger = zap.NewNop()
	}
	
	adapter := &FederatedFUSEAdapter{
		localDomain:   localDomain,
		connManager:   connManager,
		permManager:   permManager,
		placement:     placement,
		logger:        logger,
		prefetchQueue: make(chan PrefetchRequest, 100),
		readAhead:     true,
		cacheSize:     1024 * 1024 * 1024, // 1GB default cache
	}
	
	// Initialize cache
	adapter.cache = NewMediaCache(adapter.cacheSize)
	
	// Start prefetch worker
	go adapter.prefetchWorker()
	
	return adapter
}

// ReadFile reads a file from federated storage
func (fa *FederatedFUSEAdapter) ReadFile(ctx context.Context, path string, offset int64, size int) ([]byte, error) {
	// Parse path to get federated address
	addr, err := fa.parsePathToAddress(path)
	if err != nil {
		return nil, fmt.Errorf("invalid federated path: %w", err)
	}
	
	// Check permissions
	caller := fa.getCurrentUser()
	hasAccess, err := fa.permManager.CheckPermission(path, caller, RightRead)
	if err != nil {
		return nil, err
	}
	if !hasAccess {
		return nil, fmt.Errorf("permission denied")
	}
	
	// Determine if this is a local or remote file
	if addr.IsLocal(fa.localDomain) {
		return fa.readLocalFile(ctx, path, offset, size)
	}
	
	return fa.readRemoteFile(ctx, addr, path, offset, size)
}

// readLocalFile reads from local storage
func (fa *FederatedFUSEAdapter) readLocalFile(ctx context.Context, path string, offset int64, size int) ([]byte, error) {
	// This would integrate with the existing local FUSE implementation
	// For now, return a placeholder
	return nil, fmt.Errorf("local file reading not implemented")
}

// readRemoteFile reads from remote federation member
func (fa *FederatedFUSEAdapter) readRemoteFile(ctx context.Context, addr *FederatedAddress, path string, offset int64, size int) ([]byte, error) {
	// Calculate which chunks we need
	chunkIDs := fa.calculateChunksForRange(path, offset, size)
	
	// Check cache first
	cachedData, complete := fa.readFromCache(chunkIDs)
	if complete {
		fa.logger.Debug("Cache hit for remote file",
			zap.String("path", path),
			zap.Int64("offset", offset))
		return fa.extractRange(cachedData, offset, size), nil
	}
	
	// Fetch missing chunks from remote
	data, err := fa.fetchRemoteChunks(ctx, addr, chunkIDs)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch remote chunks: %w", err)
	}
	
	// Cache for future use
	fa.cacheChunks(chunkIDs, data)
	
	// Trigger prefetch for sequential reads
	if fa.readAhead {
		fa.triggerPrefetch(path, offset+int64(size), size)
	}
	
	return fa.extractRange(data, offset, size), nil
}

// fetchRemoteChunks fetches chunks from a remote member
func (fa *FederatedFUSEAdapter) fetchRemoteChunks(ctx context.Context, addr *FederatedAddress, chunkIDs []string) ([]byte, error) {
	// Use connection manager for resilient connection
	_, err := fa.connManager.CreateCoordinatorClient(addr)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to %s: %w", addr.String(), err)
	}
	
	// Fetch chunks in parallel for performance
	var wg sync.WaitGroup
	chunks := make(map[string][]byte)
	errors := make(chan error, len(chunkIDs))
	mu := sync.Mutex{}
	
	for _, chunkID := range chunkIDs {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			
			// Fetch individual chunk
			// In a real implementation, this would use a streaming API
			// For now, we'll use a placeholder
			
			// This would actually fetch the chunk from the remote coordinator
			// For demonstration, return empty data
			chunkData := []byte{} // Placeholder
			
			mu.Lock()
			chunks[id] = chunkData
			mu.Unlock()
		}(chunkID)
	}
	
	wg.Wait()
	close(errors)
	
	// Check for errors
	for err := range errors {
		if err != nil {
			return nil, err
		}
	}
	
	// Combine chunks in order
	var result []byte
	for _, id := range chunkIDs {
		result = append(result, chunks[id]...)
	}
	
	return result, nil
}

// WriteFile writes a file to federated storage
func (fa *FederatedFUSEAdapter) WriteFile(ctx context.Context, path string, data []byte, offset int64) error {
	// Parse path to get federated address
	_, err := fa.parsePathToAddress(path)
	if err != nil {
		return fmt.Errorf("invalid federated path: %w", err)
	}
	
	// Check permissions
	caller := fa.getCurrentUser()
	hasAccess, err := fa.permManager.CheckPermission(path, caller, RightWrite)
	if err != nil {
		return err
	}
	if !hasAccess {
		return fmt.Errorf("permission denied")
	}
	
	// Use placement controller to determine where to store
	chunkSize := len(data)
	nodeIDs, err := fa.placement.PlaceChunk(path, int64(chunkSize), path)
	if err != nil {
		return fmt.Errorf("failed to determine placement: %w", err)
	}
	
	// Write to selected nodes
	return fa.writeToNodes(ctx, path, data, nodeIDs)
}

// prefetchWorker handles background prefetching
func (fa *FederatedFUSEAdapter) prefetchWorker() {
	for req := range fa.prefetchQueue {
		// Skip if past deadline
		if time.Now().After(req.Deadline) {
			continue
		}
		
		// Prefetch the chunk
		fa.prefetchChunk(req.ChunkID)
	}
}

// triggerPrefetch queues chunks for prefetching
func (fa *FederatedFUSEAdapter) triggerPrefetch(path string, nextOffset int64, size int) {
	// Calculate next chunks likely to be read
	chunkIDs := fa.calculateChunksForRange(path, nextOffset, size*2) // Prefetch double the size
	
	for _, chunkID := range chunkIDs {
		select {
		case fa.prefetchQueue <- PrefetchRequest{
			ChunkID:  chunkID,
			Priority: 1,
			Deadline: time.Now().Add(5 * time.Second),
		}:
		default:
			// Queue full, skip
		}
	}
}

// MediaCache implementation

// NewMediaCache creates a new media cache
func NewMediaCache(maxSize int64) *MediaCache {
	return &MediaCache{
		entries:     make(map[string]*CacheEntry),
		lru:         &LRUList{},
		maxSize:     maxSize,
		currentSize: 0,
	}
}

// Get retrieves a chunk from cache
func (mc *MediaCache) Get(chunkID string) ([]byte, bool) {
	mc.mu.Lock()
	defer mc.mu.Unlock()
	
	entry, exists := mc.entries[chunkID]
	if !exists {
		mc.misses++
		mc.updateHitRate()
		return nil, false
	}
	
	// Update access time and move to front
	entry.LastAccessed = time.Now()
	entry.AccessCount++
	mc.lru.moveToFront(entry.node)
	
	mc.hits++
	mc.updateHitRate()
	
	return entry.Data, true
}

// Put adds a chunk to cache
func (mc *MediaCache) Put(chunkID string, data []byte, priority CachePriority) {
	mc.mu.Lock()
	defer mc.mu.Unlock()
	
	size := int64(len(data))
	
	// Check if already exists
	if existing, exists := mc.entries[chunkID]; exists {
		// Update existing entry
		existing.Data = data
		existing.Size = size
		existing.LastAccessed = time.Now()
		existing.Priority = priority
		mc.lru.moveToFront(existing.node)
		return
	}
	
	// Evict if necessary
	for mc.currentSize+size > mc.maxSize && len(mc.entries) > 0 {
		mc.evictLRU()
	}
	
	// Add new entry
	entry := &CacheEntry{
		ChunkID:      chunkID,
		Data:         data,
		Size:         size,
		LastAccessed: time.Now(),
		AccessCount:  1,
		Priority:     priority,
	}
	
	node := mc.lru.pushFront(entry)
	entry.node = node
	
	mc.entries[chunkID] = entry
	mc.currentSize += size
}

// evictLRU evicts the least recently used entry
func (mc *MediaCache) evictLRU() {
	if mc.lru.tail == nil {
		return
	}
	
	// Skip pinned entries
	node := mc.lru.tail
	for node != nil && node.entry.Priority == PriorityPinned {
		node = node.prev
	}
	
	if node == nil {
		// All entries are pinned, evict tail anyway
		node = mc.lru.tail
	}
	
	entry := node.entry
	mc.lru.remove(node)
	delete(mc.entries, entry.ChunkID)
	mc.currentSize -= entry.Size
}

// updateHitRate updates cache hit rate statistics
func (mc *MediaCache) updateHitRate() {
	total := float64(mc.hits + mc.misses)
	if total > 0 {
		mc.hitRate = float64(mc.hits) / total
		mc.missRate = float64(mc.misses) / total
	}
}

// GetStatistics returns cache statistics
func (mc *MediaCache) GetStatistics() map[string]interface{} {
	mc.mu.RLock()
	defer mc.mu.RUnlock()
	
	return map[string]interface{}{
		"entries":      len(mc.entries),
		"size":         mc.currentSize,
		"max_size":     mc.maxSize,
		"hit_rate":     mc.hitRate,
		"miss_rate":    mc.missRate,
		"total_hits":   mc.hits,
		"total_misses": mc.misses,
	}
}

// LRU list operations

func (l *LRUList) pushFront(entry *CacheEntry) *LRUNode {
	node := &LRUNode{entry: entry}
	
	if l.head == nil {
		l.head = node
		l.tail = node
	} else {
		node.next = l.head
		l.head.prev = node
		l.head = node
	}
	
	return node
}

func (l *LRUList) moveToFront(node *LRUNode) {
	if node == l.head {
		return
	}
	
	l.remove(node)
	
	node.prev = nil
	node.next = l.head
	l.head.prev = node
	l.head = node
}

func (l *LRUList) remove(node *LRUNode) {
	if node.prev != nil {
		node.prev.next = node.next
	} else {
		l.head = node.next
	}
	
	if node.next != nil {
		node.next.prev = node.prev
	} else {
		l.tail = node.prev
	}
}

// Helper methods

func (fa *FederatedFUSEAdapter) parsePathToAddress(path string) (*FederatedAddress, error) {
	// Parse federated paths like: /federation/alice@home.collective.local/media/movie.mp4
	// This is simplified - real implementation would be more sophisticated
	if len(path) > 11 && path[:11] == "/federation" {
		parts := strings.SplitN(path[11:], "/", 3)
		if len(parts) >= 2 {
			return ParseAddress(parts[1])
		}
	}
	
	// Default to local domain
	return ParseAddress("coord@" + fa.localDomain)
}

func (fa *FederatedFUSEAdapter) getCurrentUser() *FederatedAddress {
	// In production, this would get the authenticated user
	addr, _ := ParseAddress("user@" + fa.localDomain)
	return addr
}

func (fa *FederatedFUSEAdapter) calculateChunksForRange(path string, offset int64, size int) []string {
	// Calculate which chunks are needed for the given byte range
	// This is simplified - real implementation would use actual chunk metadata
	chunkSize := int64(1024 * 1024) // 1MB chunks
	startChunk := offset / chunkSize
	endChunk := (offset + int64(size)) / chunkSize
	
	chunks := []string{}
	for i := startChunk; i <= endChunk; i++ {
		chunks = append(chunks, fmt.Sprintf("%s-chunk-%d", path, i))
	}
	
	return chunks
}

func (fa *FederatedFUSEAdapter) readFromCache(chunkIDs []string) ([]byte, bool) {
	var result []byte
	
	for _, id := range chunkIDs {
		data, exists := fa.cache.Get(id)
		if !exists {
			return nil, false
		}
		result = append(result, data...)
	}
	
	return result, true
}

func (fa *FederatedFUSEAdapter) cacheChunks(chunkIDs []string, data []byte) {
	// Cache individual chunks
	// This is simplified - real implementation would split data properly
	chunkSize := len(data) / len(chunkIDs)
	
	for i, id := range chunkIDs {
		start := i * chunkSize
		end := start + chunkSize
		if end > len(data) {
			end = len(data)
		}
		
		fa.cache.Put(id, data[start:end], PriorityNormal)
	}
}

func (fa *FederatedFUSEAdapter) extractRange(data []byte, offset int64, size int) []byte {
	// Extract the requested range from the data
	if int(offset) >= len(data) {
		return []byte{}
	}
	
	end := int(offset) + size
	if end > len(data) {
		end = len(data)
	}
	
	return data[offset:end]
}

func (fa *FederatedFUSEAdapter) writeToNodes(ctx context.Context, path string, data []byte, nodeIDs []types.NodeID) error {
	// Write data to specified nodes
	// This would integrate with the existing storage system
	return fmt.Errorf("write to nodes not implemented")
}

func (fa *FederatedFUSEAdapter) prefetchChunk(chunkID string) {
	// Prefetch a chunk in the background
	// This would fetch and cache the chunk
	fa.logger.Debug("Prefetching chunk", zap.String("chunk", chunkID))
}

// StreamingReader provides optimized streaming for media files
type StreamingReader struct {
	adapter      *FederatedFUSEAdapter
	path         string
	size         int64
	position     int64
	bufferSize   int
	readAhead    chan []byte
	mu           sync.Mutex
}

// NewStreamingReader creates a reader optimized for media streaming
func (adapter *FederatedFUSEAdapter) NewStreamingReader(path string, size int64) io.ReadCloser {
	sr := &StreamingReader{
		adapter:    adapter,
		path:       path,
		size:       size,
		position:   0,
		bufferSize: 64 * 1024, // 64KB buffer
		readAhead:  make(chan []byte, 4),
	}
	
	// Start read-ahead worker
	go sr.readAheadWorker()
	
	return sr
}

// Read implements io.Reader for streaming
func (sr *StreamingReader) Read(p []byte) (n int, err error) {
	sr.mu.Lock()
	defer sr.mu.Unlock()
	
	if sr.position >= sr.size {
		return 0, io.EOF
	}
	
	// Try to get from read-ahead buffer
	select {
	case data := <-sr.readAhead:
		n = copy(p, data)
		sr.position += int64(n)
		return n, nil
	default:
	}
	
	// Read directly
	readSize := len(p)
	if int64(readSize) > sr.size-sr.position {
		readSize = int(sr.size - sr.position)
	}
	
	data, err := sr.adapter.ReadFile(context.Background(), sr.path, sr.position, readSize)
	if err != nil {
		return 0, err
	}
	
	n = copy(p, data)
	sr.position += int64(n)
	
	if sr.position >= sr.size {
		return n, io.EOF
	}
	
	return n, nil
}

// Close implements io.Closer
func (sr *StreamingReader) Close() error {
	close(sr.readAhead)
	return nil
}

// readAheadWorker prefetches data for smooth streaming
func (sr *StreamingReader) readAheadWorker() {
	for sr.position < sr.size {
		// Read ahead the next buffer
		nextPos := sr.position + int64(sr.bufferSize)
		if nextPos >= sr.size {
			break
		}
		
		data, err := sr.adapter.ReadFile(context.Background(), sr.path, nextPos, sr.bufferSize)
		if err != nil {
			break
		}
		
		select {
		case sr.readAhead <- data:
		default:
			// Buffer full, wait
			time.Sleep(10 * time.Millisecond)
		}
	}
}