package config

import (
	"fmt"
	"os"
	"strings"

	"collective/pkg/auth"
)

type Mode string

const (
	ModeCoordinator Mode = "coordinator"
	ModeNode        Mode = "node"
)

type Config struct {
	Mode        Mode              `json:"mode"`
	MemberID    string            `json:"member_id"`
	Coordinator CoordinatorConfig `json:"coordinator,omitempty"`
	Node        NodeConfig        `json:"node,omitempty"`
	Auth        *auth.AuthConfig  `json:"auth,omitempty"`
}

type CoordinatorConfig struct {
	Address              string       `json:"address"`
	BootstrapPeers       []PeerConfig `json:"bootstrap_peers"`
	DataDir              string       `json:"data_dir"`
	BidirectionalPeering bool         `json:"bidirectional_peering"`
}

type NodeConfig struct {
	NodeID             string `json:"node_id"`
	Address            string `json:"address"`
	CoordinatorAddress string `json:"coordinator_address"`
	StorageCapacity    int64  `json:"storage_capacity"`
	DataDir            string `json:"data_dir"`
}

type PeerConfig struct {
	MemberID string `json:"member_id"`
	Address  string `json:"address"`
}

func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	// Use enhanced loader to support human-friendly sizes
	return LoadConfigEnhanced(data)
}

func LoadFromEnv() *Config {
	cfg := &Config{
		Mode:     Mode(getEnv("COLLECTIVE_MODE", "coordinator")),
		MemberID: getEnv("COLLECTIVE_MEMBER_ID", ""),
	}

	if cfg.Mode == ModeCoordinator {
		cfg.Coordinator = CoordinatorConfig{
			Address: getEnv("COLLECTIVE_COORDINATOR_ADDRESS", ":8001"),
			DataDir: getEnv("COLLECTIVE_DATA_DIR", "./data"),
		}

		if peers := os.Getenv("COLLECTIVE_BOOTSTRAP_PEERS"); peers != "" {
			// Parse comma-separated peers: bob@172.20.0.20:8001,carol@172.20.0.30:8001
			for _, peer := range strings.Split(peers, ",") {
				peer = strings.TrimSpace(peer)
				if peer == "" {
					continue
				}
				
				// Expected format: memberID@address:port or memberID:address:port
				var memberID, address string
				if strings.Contains(peer, "@") {
					parts := strings.SplitN(peer, "@", 2)
					if len(parts) == 2 {
						memberID = parts[0]
						address = parts[1]
					}
				} else {
					// Legacy format: memberID:address:port
					parts := strings.SplitN(peer, ":", 2)
					if len(parts) == 2 {
						memberID = parts[0]
						address = parts[1]
					}
				}
				
				if memberID != "" && address != "" {
					cfg.Coordinator.BootstrapPeers = append(cfg.Coordinator.BootstrapPeers, PeerConfig{
						MemberID: memberID,
						Address:  address,
					})
				}
			}
		}
	} else {
		cfg.Node = NodeConfig{
			NodeID:             getEnv("COLLECTIVE_NODE_ID", ""),
			Address:            getEnv("COLLECTIVE_NODE_ADDRESS", ":7001"),
			CoordinatorAddress: getEnv("COLLECTIVE_COORDINATOR_ADDRESS", "localhost:8001"),
			StorageCapacity:    1073741824, // 1GB default
			DataDir:            getEnv("COLLECTIVE_DATA_DIR", "./data"),
		}
	}

	return cfg
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}
