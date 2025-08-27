package config

import (
	"collective/pkg/auth"
	"collective/pkg/utils"
	"encoding/json"
	"fmt"
)

// NodeConfigRaw represents the raw JSON structure for node config
// with storage_capacity as a string that can be parsed
type NodeConfigRaw struct {
	NodeID             string      `json:"node_id"`
	Address            string      `json:"address"`
	CoordinatorAddress string      `json:"coordinator_address"`
	StorageCapacity    interface{} `json:"storage_capacity"` // Can be string or number
	DataDir            string      `json:"data_dir"`
}

// ConfigRaw represents the raw JSON structure with flexible types
type ConfigRaw struct {
	Mode        Mode              `json:"mode"`
	MemberID    string            `json:"member_id"`
	Coordinator CoordinatorConfig `json:"coordinator,omitempty"`
	Node        NodeConfigRaw     `json:"node,omitempty"`
	Auth        json.RawMessage   `json:"auth,omitempty"`
}

// ParseNodeConfig converts NodeConfigRaw to NodeConfig, parsing storage capacity
func ParseNodeConfig(raw NodeConfigRaw) (NodeConfig, error) {
	cfg := NodeConfig{
		NodeID:             raw.NodeID,
		Address:            raw.Address,
		CoordinatorAddress: raw.CoordinatorAddress,
		DataDir:            raw.DataDir,
	}

	// Parse storage capacity - could be a number or string
	switch v := raw.StorageCapacity.(type) {
	case float64:
		// JSON numbers are parsed as float64
		cfg.StorageCapacity = int64(v)
	case int64:
		cfg.StorageCapacity = v
	case string:
		// Parse human-friendly format
		capacity, err := utils.ParseDataSize(v)
		if err != nil {
			return cfg, fmt.Errorf("invalid storage capacity format: %w", err)
		}
		cfg.StorageCapacity = capacity
	case nil:
		// Default to 1GB if not specified
		cfg.StorageCapacity = 1073741824
	default:
		return cfg, fmt.Errorf("storage_capacity must be a number or string, got %T", v)
	}

	return cfg, nil
}

// LoadConfigEnhanced loads config with support for human-friendly sizes
func LoadConfigEnhanced(data []byte) (*Config, error) {
	var raw ConfigRaw
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("failed to parse config: %w", err)
	}

	cfg := &Config{
		Mode:        raw.Mode,
		MemberID:    raw.MemberID,
		Coordinator: raw.Coordinator,
	}

	// Parse node config if present
	if raw.Mode == ModeNode {
		nodeConfig, err := ParseNodeConfig(raw.Node)
		if err != nil {
			return nil, fmt.Errorf("failed to parse node config: %w", err)
		}
		cfg.Node = nodeConfig
	}

	// Parse auth config if present
	if len(raw.Auth) > 0 {
		var authConfig auth.AuthConfig
		if err := json.Unmarshal(raw.Auth, &authConfig); err != nil {
			return nil, fmt.Errorf("failed to parse auth config: %w", err)
		}
		cfg.Auth = &authConfig
	}

	return cfg, nil
}