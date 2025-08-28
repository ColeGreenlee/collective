package coordinator

import (
	"collective/pkg/auth"
	"collective/pkg/federation"
	"collective/pkg/protocol"
	"collective/pkg/types"
	"context"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"time"

	"go.uber.org/zap"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// FederationCoordinator extends the Coordinator with federation capabilities
type FederationCoordinator struct {
	*Coordinator

	// Federation components
	trustStore       *federation.TrustStore
	gossipService    *federation.GossipService
	federationDomain string
}

// NewFederationCoordinator creates a coordinator with federation support
func NewFederationCoordinator(
	coordinator *Coordinator,
	federationDomain string,
	trustStore *federation.TrustStore,
) (*FederationCoordinator, error) {

	// Get certificate paths from auth config
	var certPath, keyPath string
	if coordinator.authConfig != nil {
		certPath = coordinator.authConfig.CertPath
		keyPath = coordinator.authConfig.KeyPath
	}

	// Create gossip service with trust store for secure connections
	gossipService, err := federation.NewGossipService(
		string(coordinator.memberID)+"."+federationDomain,
		trustStore,
		certPath,
		keyPath,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create gossip service: %w", err)
	}

	fc := &FederationCoordinator{
		Coordinator:      coordinator,
		trustStore:       trustStore,
		gossipService:    gossipService,
		federationDomain: federationDomain,
	}

	// Set gossip callbacks
	gossipService.SetCallbacks(
		fc.handlePeerJoin,
		fc.handlePeerLeave,
		fc.handleDataStoreUpdate,
	)

	return fc, nil
}

// StartFederation initializes federation services
func (fc *FederationCoordinator) StartFederation(bootstrapPeers []string) error {
	// Start gossip protocol
	if err := fc.gossipService.Start(); err != nil {
		return fmt.Errorf("failed to start gossip: %w", err)
	}

	// Add bootstrap peers
	for _, peerAddr := range bootstrapPeers {
		if err := fc.addBootstrapPeer(peerAddr); err != nil {
			fc.logger.Warn("Failed to add bootstrap peer",
				zap.String("peer", peerAddr),
				zap.Error(err))
		}
	}

	// Announce ourselves to the federation
	fc.announceJoin()

	fc.logger.Info("Federation services started",
		zap.String("domain", fc.federationDomain),
		zap.Int("bootstrap_peers", len(bootstrapPeers)))

	return nil
}

// StopFederation shuts down federation services
func (fc *FederationCoordinator) StopFederation() {
	// Announce leave
	fc.announceLeave()

	// Stop gossip
	fc.gossipService.Stop()

	fc.logger.Info("Federation services stopped")
}

// addBootstrapPeer adds an initial peer for gossip
func (fc *FederationCoordinator) addBootstrapPeer(peerAddrStr string) error {
	peerAddr, err := federation.ParseAddress(peerAddrStr)
	if err != nil {
		return fmt.Errorf("invalid peer address: %w", err)
	}

	// Extract endpoint from address (simplified - in production would do DNS lookup)
	endpoint := &federation.PeerEndpoint{
		Domain:    peerAddr.Domain,
		DirectIPs: []string{peerAddr.Domain + ":8001"}, // Default coordinator port
		LastSeen:  time.Now(),
	}

	return fc.gossipService.AddPeer(peerAddr, []*federation.PeerEndpoint{endpoint})
}

// announceJoin broadcasts join message to federation
func (fc *FederationCoordinator) announceJoin() {
	// Get local endpoints
	endpoints := fc.getLocalEndpoints()

	// Create join message
	joinMsg := &protocol.GossipMessage{
		Type:          protocol.GossipType_GOSSIP_JOIN,
		SourceAddress: fmt.Sprintf("coord@%s.%s", fc.memberID, fc.federationDomain),
		Version:       1,
		Timestamp:     timestamppb.Now(),
		Payload: &protocol.GossipMessage_Join{
			Join: &protocol.JoinPayload{
				MemberDomain: string(fc.memberID) + "." + fc.federationDomain,
				Endpoints:    endpoints,
				Datastores:   fc.getDataStoreInfo(),
			},
		},
	}

	fc.gossipService.Broadcast(joinMsg)

	fc.logger.Info("Announced join to federation")
}

// announceLeave broadcasts leave message to federation
func (fc *FederationCoordinator) announceLeave() {
	leaveMsg := &protocol.GossipMessage{
		Type:          protocol.GossipType_GOSSIP_LEAVE,
		SourceAddress: fmt.Sprintf("coord@%s.%s", fc.memberID, fc.federationDomain),
		Version:       1,
		Timestamp:     timestamppb.Now(),
		Payload: &protocol.GossipMessage_Leave{
			Leave: &protocol.LeavePayload{
				MemberDomain: string(fc.memberID) + "." + fc.federationDomain,
				Reason:       "Graceful shutdown",
			},
		},
	}

	fc.gossipService.Broadcast(leaveMsg)

	fc.logger.Info("Announced leave to federation")
}

// getLocalEndpoints returns the coordinator's network endpoints
func (fc *FederationCoordinator) getLocalEndpoints() []*protocol.PeerEndpoint {
	endpoints := []*protocol.PeerEndpoint{}

	// Get all network interfaces
	if fc.address != "" {
		endpoint := &protocol.PeerEndpoint{
			Domain:    string(fc.memberID) + "." + fc.federationDomain,
			DirectIps: []string{fc.address},
		}
		endpoints = append(endpoints, endpoint)
	}

	return endpoints
}

// getDataStoreInfo returns information about local datastores
func (fc *FederationCoordinator) getDataStoreInfo() []*protocol.DataStoreInfo {
	fc.directoryMutex.RLock()
	defer fc.directoryMutex.RUnlock()

	datastores := []*protocol.DataStoreInfo{}

	// For now, just report the root directory as a datastore
	rootInfo := &protocol.DataStoreInfo{
		Path:         "/",
		OwnerAddress: fmt.Sprintf("coord@%s.%s", fc.memberID, fc.federationDomain),
		ReplicaCount: 2, // Default replication
		Strategy:     protocol.PlacementStrategy_PLACEMENT_HYBRID,
		Metadata:     map[string]string{"type": "root"},
	}

	datastores = append(datastores, rootInfo)

	return datastores
}

// Gossip event handlers

func (fc *FederationCoordinator) handlePeerJoin(peer *federation.PeerState) {
	fc.logger.Info("Federation peer joined",
		zap.String("peer", peer.Address.String()),
		zap.Int("endpoints", len(peer.Endpoints)))

	// Convert to internal Peer type
	fc.peerMutex.Lock()
	defer fc.peerMutex.Unlock()

	internalPeer := &Peer{
		MemberID:  types.MemberID(peer.Address.Domain),
		Address:   peer.Address.String(),
		LastSeen:  peer.LastSeen,
		IsHealthy: true,
	}

	fc.peers[internalPeer.MemberID] = internalPeer
}

func (fc *FederationCoordinator) handlePeerLeave(domain string) {
	fc.logger.Info("Federation peer left",
		zap.String("domain", domain))

	fc.peerMutex.Lock()
	defer fc.peerMutex.Unlock()

	delete(fc.peers, types.MemberID(domain))
}

func (fc *FederationCoordinator) handleDataStoreUpdate(ds *federation.DataStoreInfo) {
	fc.logger.Debug("DataStore update received",
		zap.String("path", ds.Path),
		zap.String("owner", ds.OwnerAddress))

	// TODO: Implement DataStore synchronization
}

// GetFederationStatus returns the current federation status
func (fc *FederationCoordinator) GetFederationStatus() map[string]interface{} {
	healthyPeers := fc.gossipService.GetHealthyPeers()

	peerInfo := make([]map[string]interface{}, len(healthyPeers))
	for i, peer := range healthyPeers {
		peerInfo[i] = map[string]interface{}{
			"domain":     peer.Address.Domain,
			"status":     peer.Status,
			"last_seen":  peer.LastSeen,
			"version":    peer.Version,
			"datastores": len(peer.DataStores),
		}
	}

	return map[string]interface{}{
		"federation_domain": fc.federationDomain,
		"local_member":      fc.memberID,
		"peer_count":        len(healthyPeers),
		"peers":             peerInfo,
		"trust_store": map[string]interface{}{
			"root_ca_loaded": fc.trustStore.GetRootCA() != nil,
			"member_cas":     fc.trustStore.ListMemberCAs(),
		},
	}
}

// =============================================================================
// Federation Certificate Management
// =============================================================================

// GetFederationCA returns the federation root CA certificate
func (fc *FederationCoordinator) GetFederationCA(ctx context.Context, req *protocol.GetFederationCARequest) (*protocol.GetFederationCAResponse, error) {
	fc.logger.Debug("GetFederationCA request")

	// Get the root CA from the trust store
	rootCA := fc.trustStore.GetRootCA()
	if rootCA == nil {
		return &protocol.GetFederationCAResponse{
			Success: false,
			Message: "Federation root CA not available",
		}, nil
	}

	// Convert certificate to PEM format
	certPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: rootCA.Raw,
	})

	return &protocol.GetFederationCAResponse{
		Success:          true,
		CaCertificate:    string(certPEM),
		FederationDomain: fc.federationDomain,
		Message:          "Federation CA retrieved successfully",
	}, nil
}

// RequestClientCertificate processes a client certificate signing request
func (fc *FederationCoordinator) RequestClientCertificate(ctx context.Context, req *protocol.RequestClientCertificateRequest) (*protocol.RequestClientCertificateResponse, error) {
	fc.logger.Debug("RequestClientCertificate request",
		zap.String("client_id", req.ClientId),
		zap.String("invite_code", req.InviteCode))

	// TODO: Validate invite code here
	// For now, we'll accept any invite code and just log it
	if req.InviteCode == "" {
		return &protocol.RequestClientCertificateResponse{
			Success: false,
			Message: "Invite code is required",
		}, nil
	}

	// TODO: Parse and validate the CSR
	if req.Csr == "" {
		return &protocol.RequestClientCertificateResponse{
			Success: false,
			Message: "Certificate signing request is required",
		}, nil
	}

	// Parse the CSR
	csrBlock, _ := pem.Decode([]byte(req.Csr))
	if csrBlock == nil || csrBlock.Type != "CERTIFICATE REQUEST" {
		return &protocol.RequestClientCertificateResponse{
			Success: false,
			Message: "Invalid CSR format",
		}, nil
	}

	csr, err := x509.ParseCertificateRequest(csrBlock.Bytes)
	if err != nil {
		return &protocol.RequestClientCertificateResponse{
			Success: false,
			Message: fmt.Sprintf("Failed to parse CSR: %v", err),
		}, nil
	}

	// Verify CSR signature
	if err := csr.CheckSignature(); err != nil {
		return &protocol.RequestClientCertificateResponse{
			Success: false,
			Message: fmt.Sprintf("CSR signature verification failed: %v", err),
		}, nil
	}

	// Get certificate paths from auth config
	var certPath, keyPath string
	if fc.authConfig != nil {
		certPath = fc.authConfig.CertPath
		keyPath = fc.authConfig.KeyPath
	}

	if certPath == "" || keyPath == "" {
		return &protocol.RequestClientCertificateResponse{
			Success: false,
			Message: "Coordinator certificate configuration missing",
		}, nil
	}

	// Load the CA certificate and private key
	certMgr := auth.NewFederationCertManager("")
	if err := certMgr.LoadCAFromFiles(certPath, keyPath); err != nil {
		fc.logger.Error("Failed to load CA certificates", zap.Error(err))
		return &protocol.RequestClientCertificateResponse{
			Success: false,
			Message: "Failed to load CA certificates for signing",
		}, nil
	}

	// Create client certificate template
	template := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().Unix()),
		Subject: pkix.Name{
			CommonName:   req.ClientId,
			Organization: []string{"Collective Storage Federation"},
		},
		NotBefore:   time.Now(),
		NotAfter:    time.Now().Add(24 * time.Hour * 365), // 1 year validity
		KeyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		DNSNames:    []string{req.ClientId},
	}

	// Sign the certificate with the CA
	certDER, err := x509.CreateCertificate(rand.Reader, template, certMgr.GetCACert(), csr.PublicKey, certMgr.GetCAKey())
	if err != nil {
		fc.logger.Error("Failed to sign certificate", zap.Error(err))
		return &protocol.RequestClientCertificateResponse{
			Success: false,
			Message: fmt.Sprintf("Certificate signing failed: %v", err),
		}, nil
	}

	// Convert certificate to PEM format
	certPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: certDER,
	})

	fc.logger.Info("Successfully signed client certificate",
		zap.String("client_id", req.ClientId),
		zap.String("invite_code", req.InviteCode))

	return &protocol.RequestClientCertificateResponse{
		Success:           true,
		ClientCertificate: string(certPEM),
		Message:           "Client certificate signed successfully",
	}, nil
}
