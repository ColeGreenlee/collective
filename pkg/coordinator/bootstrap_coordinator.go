package coordinator

import (
	"context"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"time"

	"collective/pkg/federation"
	"collective/pkg/protocol"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// BootstrapCoordinator is a limited gRPC server that only handles invite operations
// It runs on an insecure port to allow certificate-less clients to redeem invites
type BootstrapCoordinator struct {
	coordinator *Coordinator
	logger      *zap.Logger
	protocol.UnimplementedCoordinatorServer
}

// GetFederationCA returns the CA certificate for initial trust establishment
func (b *BootstrapCoordinator) GetFederationCA(ctx context.Context, req *protocol.GetFederationCARequest) (*protocol.GetFederationCAResponse, error) {
	b.logger.Info("GetFederationCA request received")

	// This is allowed without authentication - it's public information
	if b.coordinator.authConfig == nil || b.coordinator.authConfig.CAPath == "" {
		return nil, status.Error(codes.FailedPrecondition, "TLS not configured")
	}

	// Read CA certificate
	caData, err := readFile(b.coordinator.authConfig.CAPath)
	if err != nil {
		b.logger.Error("Failed to read CA certificate", zap.Error(err))
		return nil, status.Error(codes.Internal, "failed to read CA certificate")
	}

	return &protocol.GetFederationCAResponse{
		Success:          true,
		CaCertificate:    string(caData),
		FederationDomain: string(b.coordinator.memberID) + ".collective.local",
		Message:          "CA certificate retrieved successfully",
	}, nil
}

// RequestClientCertificate allows certificate-less clients to request a client certificate
func (b *BootstrapCoordinator) RequestClientCertificate(ctx context.Context, req *protocol.RequestClientCertificateRequest) (*protocol.RequestClientCertificateResponse, error) {
	b.logger.Info("RequestClientCertificate request received",
		zap.String("client_id", req.ClientId),
		zap.String("invite_code", req.InviteCode))

	// Validate invite code using the invite manager
	if b.coordinator.inviteManager == nil {
		b.logger.Error("Invite manager not initialized")
		return &protocol.RequestClientCertificateResponse{
			Success: false,
			Message: "Invite system not available",
		}, nil
	}

	// Parse the client ID into a federated address
	federatedAddr, err := federation.ParseAddress(req.ClientId)
	if err != nil {
		// If not in federated format, create local address
		federatedAddr = &federation.FederatedAddress{
			LocalPart: req.ClientId,
			Domain:    string(b.coordinator.memberID) + ".collective.local",
		}
	}

	// Redeem the invite
	invite, err := b.coordinator.inviteManager.RedeemInvite(req.InviteCode, federatedAddr)
	if err != nil {
		b.logger.Warn("Invalid invite code",
			zap.String("code", req.InviteCode),
			zap.Error(err))
		return &protocol.RequestClientCertificateResponse{
			Success: false,
			Message: fmt.Sprintf("Invalid invite code: %v", err),
		}, nil
	}

	b.logger.Info("Invite code validated",
		zap.String("client_id", req.ClientId),
		zap.String("invite_code", req.InviteCode),
		zap.String("inviter", invite.Inviter.String()))

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
		b.logger.Error("Failed to parse CSR", zap.Error(err))
		return &protocol.RequestClientCertificateResponse{
			Success: false,
			Message: "Failed to parse CSR",
		}, nil
	}

	// Verify the CSR signature
	if err := csr.CheckSignature(); err != nil {
		return &protocol.RequestClientCertificateResponse{
			Success: false,
			Message: "Invalid CSR signature",
		}, nil
	}

	// Load the actual CA certificate and key (not the coordinator's cert)
	// The CA files are at /collective/certs/ca/[member]-ca.crt and .key
	memberID := string(b.coordinator.memberID)
	caCertPath := fmt.Sprintf("/collective/certs/ca/%s-ca.crt", memberID)
	caKeyPath := fmt.Sprintf("/collective/certs/ca/%s-ca.key", memberID)

	caCertPEM, err := os.ReadFile(caCertPath)
	if err != nil {
		b.logger.Error("Failed to read CA cert", zap.Error(err))
		return &protocol.RequestClientCertificateResponse{
			Success: false,
			Message: "Internal error: failed to load CA",
		}, nil
	}

	caKeyPEM, err := os.ReadFile(caKeyPath)
	if err != nil {
		b.logger.Error("Failed to read CA key", zap.Error(err))
		return &protocol.RequestClientCertificateResponse{
			Success: false,
			Message: "Internal error: failed to load CA key",
		}, nil
	}

	// Parse CA cert
	caCertBlock, _ := pem.Decode(caCertPEM)
	if caCertBlock == nil {
		return &protocol.RequestClientCertificateResponse{
			Success: false,
			Message: "Internal error: invalid CA cert",
		}, nil
	}

	caCert, err := x509.ParseCertificate(caCertBlock.Bytes)
	if err != nil {
		return &protocol.RequestClientCertificateResponse{
			Success: false,
			Message: "Internal error: failed to parse CA cert",
		}, nil
	}

	// Parse CA key
	caKeyBlock, _ := pem.Decode(caKeyPEM)
	if caKeyBlock == nil {
		return &protocol.RequestClientCertificateResponse{
			Success: false,
			Message: "Internal error: invalid CA key",
		}, nil
	}

	caKey, err := x509.ParsePKCS8PrivateKey(caKeyBlock.Bytes)
	if err != nil {
		// Try parsing as PKCS1 RSA key
		caKey, err = x509.ParsePKCS1PrivateKey(caKeyBlock.Bytes)
		if err != nil {
			// Try parsing as EC private key
			caKey, err = x509.ParseECPrivateKey(caKeyBlock.Bytes)
			if err != nil {
				b.logger.Error("Failed to parse CA key", zap.Error(err))
				return &protocol.RequestClientCertificateResponse{
					Success: false,
					Message: "Internal error: failed to parse CA key",
				}, nil
			}
		}
	}

	// Create the certificate template
	template := x509.Certificate{
		SerialNumber: big.NewInt(time.Now().Unix()),
		Subject: pkix.Name{
			Organization: []string{"Collective Federation"},
			Country:      []string{"US"},
			CommonName:   req.ClientId,
		},
		NotBefore:   time.Now(),
		NotAfter:    time.Now().Add(365 * 24 * time.Hour), // 1 year validity
		KeyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		IsCA:        false,
	}

	// Sign the certificate
	certDER, err := x509.CreateCertificate(rand.Reader, &template, caCert, csr.PublicKey, caKey)
	if err != nil {
		b.logger.Error("Failed to create certificate", zap.Error(err))
		return &protocol.RequestClientCertificateResponse{
			Success: false,
			Message: "Failed to create certificate",
		}, nil
	}

	// Encode certificate to PEM
	certPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: certDER,
	})

	// Read the actual CA certificate (not coordinator cert)
	actualCAPath := b.coordinator.authConfig.CAPath
	actualCAPEM, err := os.ReadFile(actualCAPath)
	if err != nil {
		b.logger.Error("Failed to read actual CA", zap.Error(err))
		actualCAPEM = caCertPEM // Fallback to coordinator cert
	}

	b.logger.Info("Successfully signed client certificate",
		zap.String("client_id", req.ClientId),
		zap.String("serial", template.SerialNumber.String()))

	return &protocol.RequestClientCertificateResponse{
		Success:           true,
		Message:           "Certificate issued successfully",
		ClientCertificate: string(certPEM),
		CaCertificate:     string(actualCAPEM),
		CollectiveName:    string(b.coordinator.memberID) + "-collective",
	}, nil
}

// All other methods return Unimplemented for security
func (b *BootstrapCoordinator) RegisterNode(ctx context.Context, req *protocol.RegisterNodeRequest) (*protocol.RegisterNodeResponse, error) {
	return nil, status.Error(codes.Unimplemented, "bootstrap server only handles invite operations")
}

func (b *BootstrapCoordinator) Heartbeat(ctx context.Context, req *protocol.HeartbeatRequest) (*protocol.HeartbeatResponse, error) {
	return nil, status.Error(codes.Unimplemented, "bootstrap server only handles invite operations")
}

func (b *BootstrapCoordinator) StoreFile(ctx context.Context, req *protocol.StoreFileRequest) (*protocol.StoreFileResponse, error) {
	return nil, status.Error(codes.Unimplemented, "bootstrap server only handles invite operations")
}

func (b *BootstrapCoordinator) WriteFileStream(stream protocol.Coordinator_WriteFileStreamServer) error {
	return status.Error(codes.Unimplemented, "bootstrap server only handles invite operations")
}

func (b *BootstrapCoordinator) RetrieveFile(ctx context.Context, req *protocol.RetrieveFileRequest) (*protocol.RetrieveFileResponse, error) {
	return nil, status.Error(codes.Unimplemented, "bootstrap server only handles invite operations")
}

func (b *BootstrapCoordinator) ReadFileStream(req *protocol.ReadFileStreamRequest, stream protocol.Coordinator_ReadFileStreamServer) error {
	return status.Error(codes.Unimplemented, "bootstrap server only handles invite operations")
}

func (b *BootstrapCoordinator) DeleteFile(ctx context.Context, req *protocol.DeleteFileRequest) (*protocol.DeleteFileResponse, error) {
	return nil, status.Error(codes.Unimplemented, "bootstrap server only handles invite operations")
}

func (b *BootstrapCoordinator) ListDirectory(ctx context.Context, req *protocol.ListDirectoryRequest) (*protocol.ListDirectoryResponse, error) {
	return nil, status.Error(codes.Unimplemented, "bootstrap server only handles invite operations")
}

func (b *BootstrapCoordinator) GetStatus(ctx context.Context, req *protocol.GetStatusRequest) (*protocol.GetStatusResponse, error) {
	return nil, status.Error(codes.Unimplemented, "bootstrap server only handles invite operations")
}

func (b *BootstrapCoordinator) PeerConnect(ctx context.Context, req *protocol.PeerConnectRequest) (*protocol.PeerConnectResponse, error) {
	return nil, status.Error(codes.Unimplemented, "bootstrap server only handles invite operations")
}

func (b *BootstrapCoordinator) SyncState(ctx context.Context, req *protocol.SyncStateRequest) (*protocol.SyncStateResponse, error) {
	return nil, status.Error(codes.Unimplemented, "bootstrap server only handles invite operations")
}

func (b *BootstrapCoordinator) CreateDirectory(ctx context.Context, req *protocol.CreateDirectoryRequest) (*protocol.CreateDirectoryResponse, error) {
	return nil, status.Error(codes.Unimplemented, "bootstrap server only handles invite operations")
}

// Helper function to read file
func readFile(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read file %s: %w", path, err)
	}
	return data, nil
}
