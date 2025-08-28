package federation

import (
	"collective/pkg/protocol"
	"fmt"
	"testing"
	"time"
	
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestGossipService_NewGossipService(t *testing.T) {
	gs, err := NewGossipService("alice.collective.local", nil, "", "")
	if err != nil {
		t.Fatalf("Failed to create gossip service: %v", err)
	}
	
	if gs.localDomain != "alice.collective.local" {
		t.Errorf("Expected domain alice.collective.local, got %s", gs.localDomain)
	}
	
	if gs.localAddress == nil {
		t.Error("Local address not set")
	}
	
	if gs.fanout != 3 {
		t.Errorf("Expected fanout 3, got %d", gs.fanout)
	}
}

func TestGossipService_AddRemovePeer(t *testing.T) {
	gs, err := NewGossipService("alice.collective.local", nil, "", "")
	if err != nil {
		t.Fatal(err)
	}
	
	// Create peer address
	bobAddr, err := ParseAddress("coord@bob.collective.local")
	if err != nil {
		t.Fatal(err)
	}
	
	// Add peer
	endpoints := []*PeerEndpoint{
		{
			Domain:    "bob.collective.local",
			DirectIPs: []string{"10.0.0.2:8001"},
			LastSeen:  time.Now(),
		},
	}
	
	err = gs.AddPeer(bobAddr, endpoints)
	if err != nil {
		t.Errorf("Failed to add peer: %v", err)
	}
	
	// Check peer was added
	if len(gs.peers) != 1 {
		t.Errorf("Expected 1 peer, got %d", len(gs.peers))
	}
	
	// Get healthy peers
	healthy := gs.GetHealthyPeers()
	if len(healthy) != 1 {
		t.Errorf("Expected 1 healthy peer, got %d", len(healthy))
	}
	
	// Remove peer
	gs.RemovePeer("bob.collective.local")
	
	if len(gs.peers) != 0 {
		t.Errorf("Expected 0 peers after removal, got %d", len(gs.peers))
	}
}

func TestGossipService_LamportClock(t *testing.T) {
	gs, err := NewGossipService("alice.collective.local", nil, "", "")
	if err != nil {
		t.Fatal(err)
	}
	
	// Initial version should be 0
	if gs.version != 0 {
		t.Errorf("Expected initial version 0, got %d", gs.version)
	}
	
	// Add peer should increment version
	bobAddr, _ := ParseAddress("coord@bob.collective.local")
	gs.AddPeer(bobAddr, nil)
	
	if gs.version != 1 {
		t.Errorf("Expected version 1 after adding peer, got %d", gs.version)
	}
	
	// Process message with higher version
	msg := &protocol.GossipMessage{
		Version: 10,
		Type:    protocol.GossipType_GOSSIP_HEARTBEAT,
	}
	
	gs.processGossipMessage(msg)
	
	// Version should be updated to msg version + 1
	if gs.version != 11 {
		t.Errorf("Expected version 11 after processing message, got %d", gs.version)
	}
}

func TestGossipService_PrepareGossipMessage(t *testing.T) {
	gs, err := NewGossipService("alice.collective.local", nil, "", "")
	if err != nil {
		t.Fatal(err)
	}
	
	// Add some peers
	bobAddr, _ := ParseAddress("coord@bob.collective.local")
	carolAddr, _ := ParseAddress("coord@carol.collective.local")
	
	gs.AddPeer(bobAddr, nil)
	gs.AddPeer(carolAddr, nil)
	
	// Prepare gossip message
	msg := gs.prepareGossipMessage()
	
	if msg.Type != protocol.GossipType_GOSSIP_HEARTBEAT {
		t.Errorf("Expected HEARTBEAT message type, got %v", msg.Type)
	}
	
	heartbeat := msg.GetHeartbeat()
	if heartbeat == nil {
		t.Fatal("Expected heartbeat payload")
	}
	
	// Should contain both peers
	if len(heartbeat.KnownPeers) != 2 {
		t.Errorf("Expected 2 known peers, got %d", len(heartbeat.KnownPeers))
	}
	
	// Check version vector
	if len(heartbeat.PeerVersions) != 2 {
		t.Errorf("Expected 2 peer versions, got %d", len(heartbeat.PeerVersions))
	}
}

func TestGossipService_SelectGossipPeers(t *testing.T) {
	gs, err := NewGossipService("alice.collective.local", nil, "", "")
	if err != nil {
		t.Fatal(err)
	}
	
	// Add more peers than fanout
	for i := 0; i < 5; i++ {
		addr, _ := ParseAddress(fmt.Sprintf("coord@peer%d.collective.local", i))
		gs.AddPeer(addr, nil)
	}
	
	// Select peers for gossip
	selected := gs.selectGossipPeers()
	
	// Should select up to fanout peers
	if len(selected) != gs.fanout {
		t.Errorf("Expected %d selected peers, got %d", gs.fanout, len(selected))
	}
	
	// Selected peers should be alive
	for _, peer := range selected {
		if peer.Status != PeerAlive {
			t.Errorf("Selected peer %s is not alive", peer.Address.Domain)
		}
	}
}

func TestGossipService_HandleJoinMessage(t *testing.T) {
	gs, err := NewGossipService("alice.collective.local", nil, "", "")
	if err != nil {
		t.Fatal(err)
	}
	
	// Track join callback
	joinCalled := false
	var joinedPeer *PeerState
	gs.SetCallbacks(
		func(peer *PeerState) {
			joinCalled = true
			joinedPeer = peer
		},
		nil,
		nil,
	)
	
	// Create join message
	joinMsg := &protocol.GossipMessage{
		Type:          protocol.GossipType_GOSSIP_JOIN,
		SourceAddress: "coord@bob.collective.local",
		Version:       1,
		Timestamp:     timestamppb.Now(),
		Payload: &protocol.GossipMessage_Join{
			Join: &protocol.JoinPayload{
				MemberDomain: "bob.collective.local",
				Endpoints: []*protocol.PeerEndpoint{
					{
						Domain:    "bob.collective.local",
						DirectIps: []string{"10.0.0.2:8001"},
					},
				},
			},
		},
	}
	
	// Process join message
	gs.processGossipMessage(joinMsg)
	
	// Wait for callback
	time.Sleep(10 * time.Millisecond)
	
	// Verify peer was added
	if len(gs.peers) != 1 {
		t.Errorf("Expected 1 peer after join, got %d", len(gs.peers))
	}
	
	// Check callback was triggered
	if !joinCalled {
		t.Error("Join callback was not called")
	}
	
	if joinedPeer == nil || joinedPeer.Address.Domain != "bob.collective.local" {
		t.Error("Join callback received wrong peer")
	}
}

func TestGossipService_FailureDetection(t *testing.T) {
	gs, err := NewGossipService("alice.collective.local", nil, "", "")
	if err != nil {
		t.Fatal(err)
	}
	
	// Add a peer
	bobAddr, _ := ParseAddress("coord@bob.collective.local")
	gs.AddPeer(bobAddr, nil)
	
	// Initially should be alive
	peer := gs.peers["bob.collective.local"]
	if peer.Status != PeerAlive {
		t.Error("Peer should initially be alive")
	}
	
	// Simulate time passing without heartbeat (45 seconds - between suspect and dead)
	peer.LastSeen = time.Now().Add(-45 * time.Second)
	
	// Run failure detection
	gs.detectFailures(30*time.Second, 60*time.Second)
	
	// Peer should be suspected (value 2)
	if peer.Status != PeerSuspected {
		t.Errorf("Peer should be suspected (status=%d), got %d", PeerSuspected, peer.Status)
	}
	
	// More time passes
	peer.LastSeen = time.Now().Add(-5 * time.Minute)
	gs.detectFailures(30*time.Second, 60*time.Second)
	
	// Peer should be dead
	if peer.Status != PeerDead {
		t.Errorf("Peer should be dead, got %v", peer.Status)
	}
}

func TestGossipService_VersionVector(t *testing.T) {
	gs, err := NewGossipService("alice.collective.local", nil, "", "")
	if err != nil {
		t.Fatal(err)
	}
	
	// Initial version vector should contain local domain
	gs.incrementVersion()
	
	if gs.versionVector["alice.collective.local"] != 1 {
		t.Errorf("Expected version 1 for local domain, got %d", 
			gs.versionVector["alice.collective.local"])
	}
	
	// Process response with version vector
	resp := &protocol.GossipResponse{
		ResponderDomain: "bob.collective.local",
		VersionVector: map[string]uint64{
			"bob.collective.local":   5,
			"carol.collective.local": 3,
		},
	}
	
	gs.processGossipResponse(resp)
	
	// Version vector should be updated
	if gs.versionVector["bob.collective.local"] != 5 {
		t.Errorf("Expected version 5 for bob, got %d",
			gs.versionVector["bob.collective.local"])
	}
	
	if gs.versionVector["carol.collective.local"] != 3 {
		t.Errorf("Expected version 3 for carol, got %d",
			gs.versionVector["carol.collective.local"])
	}
}

func TestGossipService_NetworkPartitionRecovery(t *testing.T) {
	// Create three gossip services
	alice, _ := NewGossipService("alice.collective.local", nil, "", "")
	bob, _ := NewGossipService("bob.collective.local", nil, "", "")
	_, _ = NewGossipService("carol.collective.local", nil, "", "")
	
	// Alice knows about Bob
	bobAddr, _ := ParseAddress("coord@bob.collective.local")
	alice.AddPeer(bobAddr, nil)
	
	// Bob knows about Carol
	carolAddr, _ := ParseAddress("coord@carol.collective.local")
	bob.AddPeer(carolAddr, nil)
	
	// Simulate partition - Alice can't reach Carol directly
	// But after anti-entropy with Bob, Alice should learn about Carol
	
	// Bob's heartbeat includes Carol
	heartbeatMsg := &protocol.GossipMessage{
		Type:          protocol.GossipType_GOSSIP_HEARTBEAT,
		SourceAddress: "coord@bob.collective.local",
		Version:       1,
		Timestamp:     timestamppb.Now(),
		Payload: &protocol.GossipMessage_Heartbeat{
			Heartbeat: &protocol.HeartbeatPayload{
				KnownPeers: []string{"carol.collective.local"},
				PeerVersions: map[string]uint64{
					"carol.collective.local": 1,
				},
			},
		},
	}
	
	// Alice processes Bob's heartbeat
	alice.processGossipMessage(heartbeatMsg)
	
	// Alice should now know Carol exists (version 0 means unknown)
	if _, exists := alice.versionVector["carol.collective.local"]; !exists {
		t.Error("Alice should know about Carol after heartbeat")
	}
	
	// This would trigger anti-entropy in production
	if alice.versionVector["carol.collective.local"] != 0 {
		t.Error("Alice should have version 0 for unknown peer Carol")
	}
}

// Helper for testing
func (gs *GossipService) getPeerCount() int {
	gs.mu.RLock()
	defer gs.mu.RUnlock()
	return len(gs.peers)
}

