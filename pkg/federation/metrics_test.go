package federation

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"go.uber.org/zap"
)

func TestFederationMetrics_Creation(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics := NewFederationMetrics(registry)

	// Verify metrics are created
	if metrics.ConnectionsTotal == nil {
		t.Error("ConnectionsTotal metric not created")
	}
	if metrics.GossipPeers == nil {
		t.Error("GossipPeers metric not created")
	}
	if metrics.PlacementOperations == nil {
		t.Error("PlacementOperations metric not created")
	}
	if metrics.ClusterHealth == nil {
		t.Error("ClusterHealth metric not created")
	}
}

func TestHealthMonitor_CalculateClusterHealth(t *testing.T) {
	logger := zap.NewNop()
	registry := prometheus.NewRegistry()
	metrics := NewFederationMetrics(registry)

	// Create mock components
	gossip, _ := NewGossipService("test.local", nil, "", "")
	trustStore := &TrustStore{}
	connMgr := NewConnectionManager("test.local", trustStore, gossip, logger)
	placement := NewPlacementEngine("test.local")
	perms := NewPermissionManager()
	invites := NewInviteManager()
	defer invites.Stop()
	defer connMgr.Close()

	monitor := NewMetricsHealthMonitor(metrics, gossip, connMgr, placement, perms, invites, logger)

	// Simulate some healthy nodes
	connMgr.nodeScores["node1"] = &NodeScore{
		NodeID:      "node1",
		IsHealthy:   true,
		SuccessRate: 0.9,
	}
	connMgr.nodeScores["node2"] = &NodeScore{
		NodeID:      "node2",
		IsHealthy:   true,
		SuccessRate: 0.8,
	}
	connMgr.nodeScores["node3"] = &NodeScore{
		NodeID:      "node3",
		IsHealthy:   false,
		SuccessRate: 0.2,
	}

	// Perform health check
	monitor.performHealthCheck()

	health, _ := monitor.GetHealth()

	// Health should be reasonable (>30 due to base health)
	if health < 30 {
		t.Errorf("Expected health > 30, got %f", health)
	}
	if health > 100 {
		t.Errorf("Health should not exceed 100, got %f", health)
	}
}

func TestHealthEndpoint_Handlers(t *testing.T) {
	logger := zap.NewNop()
	registry := prometheus.NewRegistry()
	metrics := NewFederationMetrics(registry)

	monitor := NewMetricsHealthMonitor(metrics, nil, nil, nil, nil, nil, logger)
	monitor.clusterHealth = 85.5
	monitor.lastCheck = time.Now()

	endpoint := NewHealthEndpoint(monitor, logger)

	// Test health endpoint
	t.Run("Health", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/health", nil)
		w := httptest.NewRecorder()

		endpoint.handleHealth(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("Expected status 200, got %d", w.Code)
		}

		body := w.Body.String()
		if !strings.Contains(body, "healthy") {
			t.Error("Response should contain 'healthy' status")
		}
		if !strings.Contains(body, "85.5") {
			t.Error("Response should contain health score")
		}
	})

	// Test liveness endpoint
	t.Run("Liveness", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/health/live", nil)
		w := httptest.NewRecorder()

		endpoint.handleLiveness(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("Expected status 200, got %d", w.Code)
		}

		if w.Body.String() != "OK" {
			t.Errorf("Expected 'OK', got %s", w.Body.String())
		}
	})

	// Test readiness endpoint
	t.Run("Readiness", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/health/ready", nil)
		w := httptest.NewRecorder()

		endpoint.handleReadiness(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("Expected status 200, got %d", w.Code)
		}

		if w.Body.String() != "READY" {
			t.Errorf("Expected 'READY', got %s", w.Body.String())
		}
	})

	// Test readiness with unhealthy cluster
	t.Run("NotReady", func(t *testing.T) {
		monitor.clusterHealth = 20.0 // Unhealthy

		req := httptest.NewRequest("GET", "/health/ready", nil)
		w := httptest.NewRecorder()

		endpoint.handleReadiness(w, req)

		if w.Code != http.StatusServiceUnavailable {
			t.Errorf("Expected status 503, got %d", w.Code)
		}

		if w.Body.String() != "NOT READY" {
			t.Errorf("Expected 'NOT READY', got %s", w.Body.String())
		}
	})
}

func TestMetrics_Updates(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics := NewFederationMetrics(registry)

	// Update some metrics
	metrics.ConnectionsTotal.Set(5)
	metrics.ConnectionsHealthy.Set(4)
	metrics.ConnectionsUnhealthy.Set(1)
	metrics.GossipPeers.Set(3)
	metrics.ClusterHealth.Set(75.5)

	// Verify values
	value := testutil.ToFloat64(metrics.ConnectionsTotal)
	if value != 5 {
		t.Errorf("Expected ConnectionsTotal=5, got %f", value)
	}

	value = testutil.ToFloat64(metrics.ConnectionsHealthy)
	if value != 4 {
		t.Errorf("Expected ConnectionsHealthy=4, got %f", value)
	}

	value = testutil.ToFloat64(metrics.ClusterHealth)
	if value != 75.5 {
		t.Errorf("Expected ClusterHealth=75.5, got %f", value)
	}
}

func TestMetrics_NodeHealth(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics := NewFederationMetrics(registry)

	// Set node health for multiple nodes
	metrics.NodeHealth.WithLabelValues("node1", "domain1").Set(100)
	metrics.NodeHealth.WithLabelValues("node2", "domain1").Set(80)
	metrics.NodeHealth.WithLabelValues("node3", "domain2").Set(50)

	// Verify we can retrieve values
	value := testutil.ToFloat64(metrics.NodeHealth.WithLabelValues("node1", "domain1"))
	if value != 100 {
		t.Errorf("Expected node1 health=100, got %f", value)
	}

	value = testutil.ToFloat64(metrics.NodeHealth.WithLabelValues("node3", "domain2"))
	if value != 50 {
		t.Errorf("Expected node3 health=50, got %f", value)
	}
}

func TestHealthMonitor_CalculateNodeHealth(t *testing.T) {
	logger := zap.NewNop()
	registry := prometheus.NewRegistry()
	metrics := NewFederationMetrics(registry)
	monitor := NewMetricsHealthMonitor(metrics, nil, nil, nil, nil, nil, logger)

	tests := []struct {
		name     string
		peer     *PeerState
		expected float64
		margin   float64
	}{
		{
			name: "Healthy peer",
			peer: &PeerState{
				LastSeen: time.Now(),
				Status:   PeerAlive,
			},
			expected: 100,
			margin:   0,
		},
		{
			name: "Recently seen",
			peer: &PeerState{
				LastSeen: time.Now().Add(-30 * time.Second),
				Status:   PeerAlive,
			},
			expected: 100,
			margin:   0,
		},
		{
			name: "Not seen for 2 minutes",
			peer: &PeerState{
				LastSeen: time.Now().Add(-2 * time.Minute),
				Status:   PeerAlive,
			},
			expected: 80,
			margin:   0,
		},
		{
			name: "Not seen for 6 minutes",
			peer: &PeerState{
				LastSeen: time.Now().Add(-6 * time.Minute),
				Status:   PeerAlive,
			},
			expected: 50,
			margin:   0,
		},
		{
			name: "Suspected peer",
			peer: &PeerState{
				LastSeen: time.Now(),
				Status:   PeerSuspected,
			},
			expected: 50,
			margin:   0,
		},
		{
			name: "Dead peer",
			peer: &PeerState{
				LastSeen: time.Now().Add(-10 * time.Minute),
				Status:   PeerDead,
			},
			expected: 0,
			margin:   0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			health := monitor.calculateNodeHealth(tt.peer)
			if health < tt.expected-tt.margin || health > tt.expected+tt.margin {
				t.Errorf("Expected health ~%f, got %f", tt.expected, health)
			}
		})
	}
}

func TestAlertingRules(t *testing.T) {
	rules := GetAlertingRules()

	// Verify rules contain expected alerts
	expectedAlerts := []string{
		"FederationClusterUnhealthy",
		"FederationNodeDown",
		"FederationConnectionFailures",
		"FederationPlacementFailures",
		"FederationCircuitBreakerOpen",
	}

	for _, alert := range expectedAlerts {
		if !strings.Contains(rules, alert) {
			t.Errorf("Expected alerting rules to contain %s", alert)
		}
	}

	// Verify rules have proper structure
	if !strings.Contains(rules, "groups:") {
		t.Error("Rules should have groups section")
	}
	if !strings.Contains(rules, "severity:") {
		t.Error("Rules should have severity labels")
	}
	if !strings.Contains(rules, "annotations:") {
		t.Error("Rules should have annotations")
	}
}
