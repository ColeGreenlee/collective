package integration

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"collective/pkg/config"
	"collective/pkg/coordinator"
	"collective/pkg/node"
	"collective/pkg/protocol"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// TestNodeResilience tests that nodes can reconnect and re-register after coordinator restart
func TestNodeResilience(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	
	// Start coordinator
	coordCfg := &config.CoordinatorConfig{
		Address: "localhost:19001",
	}
	coord := coordinator.New(coordCfg, "test-coord", logger)
	
	go func() {
		if err := coord.Start(); err != nil {
			t.Logf("Coordinator start error: %v", err)
		}
	}()
	time.Sleep(100 * time.Millisecond)
	
	// Start a node
	nodeCfg := &config.NodeConfig{
		NodeID:             "test-node-1",
		Address:            "localhost:19101",
		DataDir:            "/tmp/test-node-1",
		StorageCapacity:    1024 * 1024 * 1024, // 1GB
		CoordinatorAddress: "localhost:19001",
	}
	
	// Clean up data dir
	os.RemoveAll(nodeCfg.DataDir)
	os.MkdirAll(nodeCfg.DataDir, 0755)
	defer os.RemoveAll(nodeCfg.DataDir)
	
	testNode := node.New(nodeCfg, "test-coord", logger)
	
	go func() {
		if err := testNode.Start(); err != nil {
			t.Logf("Node start error: %v", err)
		}
	}()
	time.Sleep(200 * time.Millisecond)
	
	// Connect to coordinator and verify node is registered
	conn, err := grpc.Dial("localhost:19001", grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	defer conn.Close()
	
	client := protocol.NewCoordinatorClient(conn)
	ctx := context.Background()
	
	// Get status and verify node is registered
	status, err := client.GetStatus(ctx, &protocol.GetStatusRequest{})
	require.NoError(t, err)
	assert.Equal(t, 1, len(status.LocalNodes))
	assert.Equal(t, "test-node-1", status.LocalNodes[0].NodeId)
	
	t.Log("Node successfully registered")
	
	// Stop coordinator
	coord.Stop()
	time.Sleep(1 * time.Second)
	
	t.Log("Coordinator stopped")
	
	// Restart coordinator
	coord = coordinator.New(coordCfg, "test-coord", logger)
	go func() {
		if err := coord.Start(); err != nil {
			t.Logf("Coordinator restart error: %v", err)
		}
	}()
	time.Sleep(100 * time.Millisecond)
	
	t.Log("Coordinator restarted")
	
	// Wait for node to re-register (heartbeat interval + buffer)
	time.Sleep(15 * time.Second)
	
	// Reconnect client
	conn, err = grpc.Dial("localhost:19001", grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	defer conn.Close()
	
	client = protocol.NewCoordinatorClient(conn)
	
	// Verify node has re-registered
	status, err = client.GetStatus(ctx, &protocol.GetStatusRequest{})
	require.NoError(t, err)
	assert.Equal(t, 1, len(status.LocalNodes), "Node should have re-registered after coordinator restart")
	assert.Equal(t, "test-node-1", status.LocalNodes[0].NodeId)
	assert.True(t, status.LocalNodes[0].IsHealthy)
	
	t.Log("Node successfully re-registered after coordinator restart")
	
	// Cleanup
	testNode.Stop()
	coord.Stop()
}

// TestNodeHeartbeat tests that nodes send regular heartbeats
func TestNodeHeartbeat(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	
	// Start coordinator
	coordCfg := &config.CoordinatorConfig{
		Address: "localhost:19002",
	}
	coord := coordinator.New(coordCfg, "test-coord", logger)
	
	go func() {
		if err := coord.Start(); err != nil {
			t.Logf("Coordinator start error: %v", err)
		}
	}()
	time.Sleep(100 * time.Millisecond)
	defer coord.Stop()
	
	// Start a node
	nodeCfg := &config.NodeConfig{
		NodeID:             "test-node-2",
		Address:            "localhost:19102",
		DataDir:            "/tmp/test-node-2",
		StorageCapacity:    1024 * 1024 * 1024, // 1GB
		CoordinatorAddress: "localhost:19002",
	}
	
	os.RemoveAll(nodeCfg.DataDir)
	os.MkdirAll(nodeCfg.DataDir, 0755)
	defer os.RemoveAll(nodeCfg.DataDir)
	
	testNode := node.New(nodeCfg, "test-coord", logger)
	
	go func() {
		if err := testNode.Start(); err != nil {
			t.Logf("Node start error: %v", err)
		}
	}()
	time.Sleep(200 * time.Millisecond)
	defer testNode.Stop()
	
	// Track node health over time
	conn, err := grpc.Dial("localhost:19002", grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	defer conn.Close()
	
	client := protocol.NewCoordinatorClient(conn)
	ctx := context.Background()
	
	// Check initial status
	status1, err := client.GetStatus(ctx, &protocol.GetStatusRequest{})
	require.NoError(t, err)
	require.Equal(t, 1, len(status1.LocalNodes))
	initialCapacity := status1.LocalNodes[0].UsedCapacity
	
	// Wait for a heartbeat cycle
	time.Sleep(12 * time.Second)
	
	// Check status again - heartbeat should have updated
	status2, err := client.GetStatus(ctx, &protocol.GetStatusRequest{})
	require.NoError(t, err)
	require.Equal(t, 1, len(status2.LocalNodes))
	assert.True(t, status2.LocalNodes[0].IsHealthy)
	
	// If node has stored any data, capacity should be reflected
	t.Logf("Node capacity: initial=%d, after heartbeat=%d", 
		initialCapacity, status2.LocalNodes[0].UsedCapacity)
}

// TestMultipleNodeResilience tests multiple nodes reconnecting
func TestMultipleNodeResilience(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	
	// Start coordinator
	coordCfg := &config.CoordinatorConfig{
		Address: "localhost:19003",
	}
	coord := coordinator.New(coordCfg, "test-coord", logger)
	
	go func() {
		if err := coord.Start(); err != nil {
			t.Logf("Coordinator start error: %v", err)
		}
	}()
	time.Sleep(100 * time.Millisecond)
	
	// Start multiple nodes
	nodes := []*node.Node{}
	for i := 1; i <= 3; i++ {
		nodeCfg := &config.NodeConfig{
			NodeID:             fmt.Sprintf("test-node-%d", i),
			Address:            fmt.Sprintf("localhost:1920%d", i),
			DataDir:            fmt.Sprintf("/tmp/test-node-%d", i),
			StorageCapacity:    1024 * 1024 * 1024, // 1GB
			CoordinatorAddress: "localhost:19003",
		}
		
		os.RemoveAll(nodeCfg.DataDir)
		os.MkdirAll(nodeCfg.DataDir, 0755)
		defer os.RemoveAll(nodeCfg.DataDir)
		
		n := node.New(nodeCfg, "test-coord", logger)
		nodes = append(nodes, n)
		
		go func(n *node.Node) {
			if err := n.Start(); err != nil {
				t.Logf("Node start error: %v", err)
			}
		}(n)
	}
	
	time.Sleep(500 * time.Millisecond)
	
	// Verify all nodes registered
	conn, err := grpc.Dial("localhost:19003", grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	defer conn.Close()
	
	client := protocol.NewCoordinatorClient(conn)
	ctx := context.Background()
	
	status, err := client.GetStatus(ctx, &protocol.GetStatusRequest{})
	require.NoError(t, err)
	assert.Equal(t, 3, len(status.LocalNodes))
	
	// Restart coordinator
	coord.Stop()
	time.Sleep(1 * time.Second)
	
	coord = coordinator.New(coordCfg, "test-coord", logger)
	go func() {
		if err := coord.Start(); err != nil {
			t.Logf("Coordinator restart error: %v", err)
		}
	}()
	time.Sleep(100 * time.Millisecond)
	
	// Wait for nodes to re-register
	time.Sleep(15 * time.Second)
	
	// Verify all nodes re-registered
	conn, err = grpc.Dial("localhost:19003", grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	defer conn.Close()
	
	client = protocol.NewCoordinatorClient(conn)
	
	status, err = client.GetStatus(ctx, &protocol.GetStatusRequest{})
	require.NoError(t, err)
	assert.Equal(t, 3, len(status.LocalNodes), "All nodes should re-register")
	
	// Check each node is healthy
	for _, nodeInfo := range status.LocalNodes {
		assert.True(t, nodeInfo.IsHealthy, "Node %s should be healthy", nodeInfo.NodeId)
	}
	
	// Cleanup
	for _, n := range nodes {
		n.Stop()
	}
	coord.Stop()
}