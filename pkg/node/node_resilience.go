package node

import (
	"context"
	"time"

	"collective/pkg/protocol"

	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

const (
	HeartbeatInterval     = 10 * time.Second
	RegistrationRetryInterval = 30 * time.Second
	ConnectionRetryInterval = 5 * time.Second
)

// enhancedHealthReportLoop replaces the basic health loop with heartbeat and re-registration
func (n *Node) enhancedHealthReportLoop() {
	heartbeatTicker := time.NewTicker(HeartbeatInterval)
	defer heartbeatTicker.Stop()
	
	registrationCheckTicker := time.NewTicker(RegistrationRetryInterval)
	defer registrationCheckTicker.Stop()
	
	// Track registration state
	registered := true
	lastHeartbeat := time.Now()
	
	for {
		select {
		case <-n.ctx.Done():
			return
			
		case <-heartbeatTicker.C:
			// Send heartbeat to coordinator
			if err := n.sendHeartbeat(); err != nil {
				n.logger.Warn("Failed to send heartbeat",
					zap.Error(err),
					zap.Duration("since_last", time.Since(lastHeartbeat)))
				
				// Check if error indicates we need to re-register
				if isRegistrationError(err) {
					registered = false
					n.logger.Info("Node appears unregistered, will attempt re-registration")
				}
			} else {
				lastHeartbeat = time.Now()
				registered = true
			}
			
		case <-registrationCheckTicker.C:
			// Periodically ensure we're registered
			if !registered {
				n.logger.Info("Attempting to re-register with coordinator")
				if err := n.reconnectAndRegister(); err != nil {
					n.logger.Error("Failed to re-register",
						zap.Error(err))
				} else {
					registered = true
					n.logger.Info("Successfully re-registered with coordinator")
				}
			}
		}
	}
}

// sendHeartbeat sends a health status update to the coordinator
func (n *Node) sendHeartbeat() error {
	if n.coordinatorClient == nil {
		return status.Error(codes.Unavailable, "coordinator client not initialized")
	}
	
	ctx, cancel := context.WithTimeout(n.ctx, 5*time.Second)
	defer cancel()
	
	n.chunksMutex.RLock()
	usedCapacity := n.usedCapacity
	n.chunksMutex.RUnlock()
	
	// Use the Heartbeat RPC to report health
	resp, err := n.coordinatorClient.Heartbeat(ctx, &protocol.HeartbeatRequest{
		MemberId: string(n.memberID),
		NodeInfo: &protocol.NodeInfo{
			NodeId:        string(n.nodeID),
			MemberId:      string(n.memberID),
			Address:       n.address,
			TotalCapacity: n.totalCapacity,
			UsedCapacity:  usedCapacity,
			IsHealthy:     true,
		},
	})
	
	if err != nil {
		return err
	}
	
	if !resp.Success {
		// Coordinator doesn't recognize us
		return status.Error(codes.NotFound, "node not registered with coordinator")
	}
	
	return nil
}

// reconnectAndRegister attempts to reconnect to coordinator and re-register
func (n *Node) reconnectAndRegister() error {
	// Close existing connection if any
	if n.coordinatorConn != nil {
		n.coordinatorConn.Close()
		n.coordinatorConn = nil
		n.coordinatorClient = nil
	}
	
	// Attempt to reconnect
	retries := 3
	for i := 0; i < retries; i++ {
		conn, err := grpc.Dial(n.coordinatorAddress, 
			grpc.WithTransportCredentials(insecure.NewCredentials()),
			grpc.WithBlock(),
			grpc.WithTimeout(5*time.Second))
		
		if err == nil {
			n.coordinatorConn = conn
			n.coordinatorClient = protocol.NewCoordinatorClient(conn)
			
			// Now attempt registration
			if err := n.registerWithCoordinator(); err != nil {
				n.logger.Warn("Registration failed, will retry",
					zap.Error(err),
					zap.Int("attempt", i+1))
				time.Sleep(ConnectionRetryInterval)
				continue
			}
			
			n.logger.Info("Successfully reconnected and registered with coordinator")
			return nil
		}
		
		n.logger.Warn("Failed to connect to coordinator, retrying",
			zap.Error(err),
			zap.Int("attempt", i+1),
			zap.Int("max_retries", retries))
		
		if i < retries-1 {
			time.Sleep(ConnectionRetryInterval)
		}
	}
	
	return status.Error(codes.Unavailable, "failed to reconnect to coordinator after retries")
}

// isRegistrationError checks if an error indicates the node needs to re-register
func isRegistrationError(err error) bool {
	if err == nil {
		return false
	}
	
	st, ok := status.FromError(err)
	if !ok {
		return false
	}
	
	switch st.Code() {
	case codes.NotFound, codes.Unauthenticated, codes.PermissionDenied:
		return true
	case codes.Unavailable:
		// Connection issues might mean coordinator restarted
		return true
	default:
		return false
	}
}

// MonitorCoordinatorConnection monitors the coordinator connection and reconnects if needed
func (n *Node) MonitorCoordinatorConnection() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	
	for {
		select {
		case <-n.ctx.Done():
			return
		case <-ticker.C:
			// Check connection state
			if n.coordinatorConn != nil {
				state := n.coordinatorConn.GetState()
				n.logger.Debug("Coordinator connection state",
					zap.String("state", state.String()))
				
				// If connection is failed or shutdown, attempt reconnect
				if state == connectivity.TransientFailure || 
				   state == connectivity.Shutdown {
					n.logger.Info("Coordinator connection lost, attempting reconnect")
					if err := n.reconnectAndRegister(); err != nil {
						n.logger.Error("Failed to reconnect to coordinator",
							zap.Error(err))
					}
				}
			}
		}
	}
}