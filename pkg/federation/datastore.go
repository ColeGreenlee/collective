package federation

import "time"

// DataStore represents a federated data store with permissions
type DataStore struct {
	Path        string
	Owner       *FederatedAddress
	Permissions []Permission
	Replicas    int
	Strategy    PlacementStrategy
	Metadata    map[string]string
}

// DataStoreInfo is a simplified version for gossip
type DataStoreInfo struct {
	Path         string
	OwnerAddress string
	ReplicaCount int
	Strategy     PlacementStrategy
	SizeBytes    int64
	Metadata     map[string]string
}

// Permission defines access rights for a DataStore
type Permission struct {
	Subject    *FederatedAddress
	Rights     []Right
	ValidUntil *time.Time
}

// Right represents an access right
type Right string

const (
	RightRead   Right = "READ"
	RightWrite  Right = "WRITE"
	RightDelete Right = "DELETE"
	RightShare  Right = "SHARE"
)

// PlacementStrategy defines chunk placement strategy
type PlacementStrategy int

const (
	PlacementUnknown PlacementStrategy = iota
	PlacementMedia    // Optimize for streaming
	PlacementBackup   // Optimize for durability
	PlacementHybrid   // Balance both
)

