package federation

import (
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.uber.org/zap"
)

// FederationMetrics tracks federation-wide metrics
type FederationMetrics struct {
	// Connection metrics
	ConnectionsTotal     prometheus.Gauge
	ConnectionsHealthy   prometheus.Gauge
	ConnectionsUnhealthy prometheus.Gauge
	ConnectionAttempts   prometheus.Counter
	ConnectionFailures   prometheus.Counter
	
	// Gossip metrics
	GossipPeers          prometheus.Gauge
	GossipMessages       prometheus.Counter
	GossipLatency        prometheus.Histogram
	GossipFailures       prometheus.Counter
	
	// Placement metrics
	PlacementOperations  prometheus.Counter
	PlacementFailures    prometheus.Counter
	PlacementLatency     prometheus.Histogram
	ChunksPerNode        *prometheus.GaugeVec
	
	// Permission metrics
	PermissionChecks     prometheus.Counter
	PermissionDenials    prometheus.Counter
	PermissionLatency    prometheus.Histogram
	DataStoresTotal      prometheus.Gauge
	
	// Invite metrics
	InvitesGenerated     prometheus.Counter
	InvitesRedeemed      prometheus.Counter
	InvitesExpired       prometheus.Counter
	InvitesActive        prometheus.Gauge
	
	// Resilience metrics
	RetryAttempts        prometheus.Counter
	CircuitBreakerOpens  prometheus.Counter
	FailoverOperations   prometheus.Counter
	FailoverSuccess      prometheus.Counter
	
	// Health metrics
	ClusterHealth        prometheus.Gauge  // 0-100 score
	NodeHealth           *prometheus.GaugeVec
	LastHealthCheck      prometheus.Gauge
}

// NewFederationMetrics creates and registers Prometheus metrics
func NewFederationMetrics(registry prometheus.Registerer) *FederationMetrics {
	if registry == nil {
		registry = prometheus.DefaultRegisterer
	}
	
	fm := &FederationMetrics{
		// Connection metrics
		ConnectionsTotal: promauto.With(registry).NewGauge(prometheus.GaugeOpts{
			Name: "federation_connections_total",
			Help: "Total number of federation connections",
		}),
		ConnectionsHealthy: promauto.With(registry).NewGauge(prometheus.GaugeOpts{
			Name: "federation_connections_healthy",
			Help: "Number of healthy federation connections",
		}),
		ConnectionsUnhealthy: promauto.With(registry).NewGauge(prometheus.GaugeOpts{
			Name: "federation_connections_unhealthy",
			Help: "Number of unhealthy federation connections",
		}),
		ConnectionAttempts: promauto.With(registry).NewCounter(prometheus.CounterOpts{
			Name: "federation_connection_attempts_total",
			Help: "Total number of connection attempts",
		}),
		ConnectionFailures: promauto.With(registry).NewCounter(prometheus.CounterOpts{
			Name: "federation_connection_failures_total",
			Help: "Total number of connection failures",
		}),
		
		// Gossip metrics
		GossipPeers: promauto.With(registry).NewGauge(prometheus.GaugeOpts{
			Name: "federation_gossip_peers",
			Help: "Number of peers in gossip network",
		}),
		GossipMessages: promauto.With(registry).NewCounter(prometheus.CounterOpts{
			Name: "federation_gossip_messages_total",
			Help: "Total number of gossip messages exchanged",
		}),
		GossipLatency: promauto.With(registry).NewHistogram(prometheus.HistogramOpts{
			Name:    "federation_gossip_latency_seconds",
			Help:    "Gossip message exchange latency",
			Buckets: prometheus.DefBuckets,
		}),
		GossipFailures: promauto.With(registry).NewCounter(prometheus.CounterOpts{
			Name: "federation_gossip_failures_total",
			Help: "Total number of gossip failures",
		}),
		
		// Placement metrics
		PlacementOperations: promauto.With(registry).NewCounter(prometheus.CounterOpts{
			Name: "federation_placement_operations_total",
			Help: "Total number of chunk placement operations",
		}),
		PlacementFailures: promauto.With(registry).NewCounter(prometheus.CounterOpts{
			Name: "federation_placement_failures_total",
			Help: "Total number of placement failures",
		}),
		PlacementLatency: promauto.With(registry).NewHistogram(prometheus.HistogramOpts{
			Name:    "federation_placement_latency_seconds",
			Help:    "Chunk placement operation latency",
			Buckets: prometheus.DefBuckets,
		}),
		ChunksPerNode: promauto.With(registry).NewGaugeVec(prometheus.GaugeOpts{
			Name: "federation_chunks_per_node",
			Help: "Number of chunks stored per node",
		}, []string{"node", "domain"}),
		
		// Permission metrics
		PermissionChecks: promauto.With(registry).NewCounter(prometheus.CounterOpts{
			Name: "federation_permission_checks_total",
			Help: "Total number of permission checks",
		}),
		PermissionDenials: promauto.With(registry).NewCounter(prometheus.CounterOpts{
			Name: "federation_permission_denials_total",
			Help: "Total number of permission denials",
		}),
		PermissionLatency: promauto.With(registry).NewHistogram(prometheus.HistogramOpts{
			Name:    "federation_permission_latency_seconds",
			Help:    "Permission check latency",
			Buckets: prometheus.DefBuckets,
		}),
		DataStoresTotal: promauto.With(registry).NewGauge(prometheus.GaugeOpts{
			Name: "federation_datastores_total",
			Help: "Total number of DataStores",
		}),
		
		// Invite metrics
		InvitesGenerated: promauto.With(registry).NewCounter(prometheus.CounterOpts{
			Name: "federation_invites_generated_total",
			Help: "Total number of invites generated",
		}),
		InvitesRedeemed: promauto.With(registry).NewCounter(prometheus.CounterOpts{
			Name: "federation_invites_redeemed_total",
			Help: "Total number of invites redeemed",
		}),
		InvitesExpired: promauto.With(registry).NewCounter(prometheus.CounterOpts{
			Name: "federation_invites_expired_total",
			Help: "Total number of invites expired",
		}),
		InvitesActive: promauto.With(registry).NewGauge(prometheus.GaugeOpts{
			Name: "federation_invites_active",
			Help: "Number of active invites",
		}),
		
		// Resilience metrics
		RetryAttempts: promauto.With(registry).NewCounter(prometheus.CounterOpts{
			Name: "federation_retry_attempts_total",
			Help: "Total number of retry attempts",
		}),
		CircuitBreakerOpens: promauto.With(registry).NewCounter(prometheus.CounterOpts{
			Name: "federation_circuit_breaker_opens_total",
			Help: "Total number of circuit breaker opens",
		}),
		FailoverOperations: promauto.With(registry).NewCounter(prometheus.CounterOpts{
			Name: "federation_failover_operations_total",
			Help: "Total number of failover operations",
		}),
		FailoverSuccess: promauto.With(registry).NewCounter(prometheus.CounterOpts{
			Name: "federation_failover_success_total",
			Help: "Total number of successful failovers",
		}),
		
		// Health metrics
		ClusterHealth: promauto.With(registry).NewGauge(prometheus.GaugeOpts{
			Name: "federation_cluster_health_score",
			Help: "Overall cluster health score (0-100)",
		}),
		NodeHealth: promauto.With(registry).NewGaugeVec(prometheus.GaugeOpts{
			Name: "federation_node_health_score",
			Help: "Individual node health score (0-100)",
		}, []string{"node", "domain"}),
		LastHealthCheck: promauto.With(registry).NewGauge(prometheus.GaugeOpts{
			Name: "federation_last_health_check_timestamp",
			Help: "Timestamp of last health check",
		}),
	}
	
	return fm
}

// MetricsHealthMonitor performs periodic health monitoring and metric collection
type MetricsHealthMonitor struct {
	metrics         *FederationMetrics
	gossip          *GossipService
	connectionMgr   *ConnectionManager
	placementEngine *PlacementEngine
	permissionMgr   *PermissionManager
	inviteMgr       *InviteManager
	logger          *zap.Logger
	
	checkInterval   time.Duration
	mu              sync.RWMutex
	lastCheck       time.Time
	clusterHealth   float64
	stopChan        chan struct{}
}

// NewMetricsHealthMonitor creates a new health monitor
func NewMetricsHealthMonitor(
	metrics *FederationMetrics,
	gossip *GossipService,
	connMgr *ConnectionManager,
	placement *PlacementEngine,
	perms *PermissionManager,
	invites *InviteManager,
	logger *zap.Logger,
) *MetricsHealthMonitor {
	if logger == nil {
		logger = zap.NewNop()
	}
	
	return &MetricsHealthMonitor{
		metrics:         metrics,
		gossip:          gossip,
		connectionMgr:   connMgr,
		placementEngine: placement,
		permissionMgr:   perms,
		inviteMgr:       invites,
		logger:          logger,
		checkInterval:   30 * time.Second,
		stopChan:        make(chan struct{}),
	}
}

// Start begins periodic health monitoring
func (hm *MetricsHealthMonitor) Start() {
	go hm.monitorLoop()
}

// Stop stops the health monitor
func (hm *MetricsHealthMonitor) Stop() {
	close(hm.stopChan)
}

// monitorLoop runs the monitoring loop
func (hm *MetricsHealthMonitor) monitorLoop() {
	ticker := time.NewTicker(hm.checkInterval)
	defer ticker.Stop()
	
	// Initial check
	hm.performHealthCheck()
	
	for {
		select {
		case <-ticker.C:
			hm.performHealthCheck()
		case <-hm.stopChan:
			return
		}
	}
}

// performHealthCheck collects all metrics and calculates health
func (hm *MetricsHealthMonitor) performHealthCheck() {
	hm.mu.Lock()
	defer hm.mu.Unlock()
	
	hm.lastCheck = time.Now()
	hm.metrics.LastHealthCheck.Set(float64(hm.lastCheck.Unix()))
	
	// Collect connection metrics
	if hm.connectionMgr != nil {
		stats := hm.connectionMgr.GetStatistics()
		if total, ok := stats["total_nodes"].(int); ok {
			hm.metrics.ConnectionsTotal.Set(float64(total))
		}
		if healthy, ok := stats["healthy_nodes"].(int); ok {
			hm.metrics.ConnectionsHealthy.Set(float64(healthy))
		}
		if unhealthy, ok := stats["unhealthy_nodes"].(int); ok {
			hm.metrics.ConnectionsUnhealthy.Set(float64(unhealthy))
		}
	}
	
	// Collect gossip metrics
	if hm.gossip != nil {
		// GetHealthyPeers returns PeerState objects
		healthyPeers := hm.gossip.GetHealthyPeers()
		hm.metrics.GossipPeers.Set(float64(len(healthyPeers)))
		
		// Update node health scores
		for _, peer := range healthyPeers {
			health := hm.calculateNodeHealth(peer)
			hm.metrics.NodeHealth.WithLabelValues(
				peer.Address.LocalPart,
				peer.Address.Domain,
			).Set(health)
		}
	}
	
	// Collect placement metrics
	if hm.placementEngine != nil {
		metrics := hm.placementEngine.GetMetrics()
		hm.metrics.PlacementOperations.Add(float64(metrics.TotalPlacements))
		hm.metrics.PlacementFailures.Add(float64(metrics.FailedPlacements))
	}
	
	// Collect permission metrics
	if hm.permissionMgr != nil {
		datastores := hm.permissionMgr.ListDataStores()
		hm.metrics.DataStoresTotal.Set(float64(len(datastores)))
	}
	
	// Collect invite metrics
	if hm.inviteMgr != nil {
		stats := hm.inviteMgr.GetInviteStats()
		if active, ok := stats["total_active"].(int); ok {
			hm.metrics.InvitesActive.Set(float64(active))
		}
	}
	
	// Calculate overall cluster health
	hm.clusterHealth = hm.calculateClusterHealth()
	hm.metrics.ClusterHealth.Set(hm.clusterHealth)
	
	hm.logger.Debug("Health check completed",
		zap.Float64("cluster_health", hm.clusterHealth),
		zap.Time("timestamp", hm.lastCheck))
}

// calculateNodeHealth calculates health score for a single node
func (hm *MetricsHealthMonitor) calculateNodeHealth(peer *PeerState) float64 {
	health := 100.0
	
	// Factor in last seen time
	timeSinceLastSeen := time.Since(peer.LastSeen)
	if timeSinceLastSeen > 5*time.Minute {
		health -= 50
	} else if timeSinceLastSeen > 1*time.Minute {
		health -= 20
	}
	
	// Factor in status
	switch peer.Status {
	case PeerDead:
		health = 0
	case PeerSuspected:
		health = health * 0.5
	}
	
	if health < 0 {
		health = 0
	}
	
	return health
}

// calculateClusterHealth calculates overall cluster health score
func (hm *MetricsHealthMonitor) calculateClusterHealth() float64 {
	factors := []float64{}
	
	// Factor 1: Connection health (30% weight)
	if hm.connectionMgr != nil {
		stats := hm.connectionMgr.GetStatistics()
		total := stats["total_nodes"].(int)
		healthy := stats["healthy_nodes"].(int)
		
		if total > 0 {
			connectionHealth := float64(healthy) / float64(total) * 100
			factors = append(factors, connectionHealth*0.3)
		}
	}
	
	// Factor 2: Gossip health (20% weight)
	if hm.gossip != nil {
		healthyPeers := hm.gossip.GetHealthyPeers()
		// Count all peers (need to access peers map size)
		// For simplicity, use healthy count as indication
		if len(healthyPeers) > 0 {
			// Assume healthy if we have any healthy peers
			gossipHealth := 100.0 // Simplified
			factors = append(factors, gossipHealth*0.2)
		}
	}
	
	// Factor 3: Placement success rate (20% weight)
	if hm.placementEngine != nil {
		metrics := hm.placementEngine.GetMetrics()
		if metrics.TotalPlacements > 0 {
			successRate := float64(metrics.TotalPlacements-metrics.FailedPlacements) / 
						  float64(metrics.TotalPlacements) * 100
			factors = append(factors, successRate*0.2)
		}
	}
	
	// Factor 4: Base health (30% weight) - system is running
	factors = append(factors, 30.0)
	
	// Sum all factors
	totalHealth := 0.0
	for _, factor := range factors {
		totalHealth += factor
	}
	
	// Cap at 100
	if totalHealth > 100 {
		totalHealth = 100
	}
	
	return totalHealth
}

// GetHealth returns the current cluster health
func (hm *MetricsHealthMonitor) GetHealth() (float64, time.Time) {
	hm.mu.RLock()
	defer hm.mu.RUnlock()
	
	return hm.clusterHealth, hm.lastCheck
}

// HealthEndpoint provides HTTP health check endpoints
type HealthEndpoint struct {
	monitor *MetricsHealthMonitor
	logger  *zap.Logger
}

// NewHealthEndpoint creates health check HTTP handlers
func NewHealthEndpoint(monitor *MetricsHealthMonitor, logger *zap.Logger) *HealthEndpoint {
	if logger == nil {
		logger = zap.NewNop()
	}
	
	return &HealthEndpoint{
		monitor: monitor,
		logger:  logger,
	}
}

// RegisterHandlers registers HTTP handlers
func (he *HealthEndpoint) RegisterHandlers(mux *http.ServeMux) {
	mux.HandleFunc("/health", he.handleHealth)
	mux.HandleFunc("/health/live", he.handleLiveness)
	mux.HandleFunc("/health/ready", he.handleReadiness)
	mux.Handle("/metrics", promhttp.Handler())
}

// handleHealth provides detailed health information
func (he *HealthEndpoint) handleHealth(w http.ResponseWriter, r *http.Request) {
	health, lastCheck := he.monitor.GetHealth()
	
	status := "healthy"
	statusCode := http.StatusOK
	
	if health < 50 {
		status = "unhealthy"
		statusCode = http.StatusServiceUnavailable
	} else if health < 80 {
		status = "degraded"
		statusCode = http.StatusOK
	}
	
	response := fmt.Sprintf(`{
		"status": "%s",
		"health_score": %.2f,
		"last_check": "%s",
		"timestamp": "%s"
	}`, status, health, lastCheck.Format(time.RFC3339), time.Now().Format(time.RFC3339))
	
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	w.Write([]byte(response))
}

// handleLiveness checks if the service is alive
func (he *HealthEndpoint) handleLiveness(w http.ResponseWriter, r *http.Request) {
	// Simple liveness check - if we can respond, we're alive
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("OK"))
}

// handleReadiness checks if the service is ready to handle requests
func (he *HealthEndpoint) handleReadiness(w http.ResponseWriter, r *http.Request) {
	health, _ := he.monitor.GetHealth()
	
	// Consider ready if health > 30%
	if health > 30 {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("READY"))
	} else {
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte("NOT READY"))
	}
}

// StartMetricsServer starts a Prometheus metrics server
func StartMetricsServer(port int, monitor *MetricsHealthMonitor, logger *zap.Logger) *http.Server {
	mux := http.NewServeMux()
	
	// Register health endpoints
	healthEndpoint := NewHealthEndpoint(monitor, logger)
	healthEndpoint.RegisterHandlers(mux)
	
	server := &http.Server{
		Addr:    fmt.Sprintf(":%d", port),
		Handler: mux,
	}
	
	go func() {
		logger.Info("Starting metrics server", zap.Int("port", port))
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("Metrics server failed", zap.Error(err))
		}
	}()
	
	return server
}

// AlertingRules provides Prometheus alerting rule templates
func GetAlertingRules() string {
	return `
groups:
  - name: federation_alerts
    interval: 30s
    rules:
      - alert: FederationClusterUnhealthy
        expr: federation_cluster_health_score < 50
        for: 5m
        labels:
          severity: critical
        annotations:
          summary: "Federation cluster health is critically low"
          description: "Cluster health score is {{ $value }}%, below critical threshold"
      
      - alert: FederationNodeDown
        expr: federation_node_health_score < 10
        for: 2m
        labels:
          severity: warning
        annotations:
          summary: "Federation node {{ $labels.node }} is down"
          description: "Node {{ $labels.node }} in domain {{ $labels.domain }} has health score {{ $value }}%"
      
      - alert: FederationConnectionFailures
        expr: rate(federation_connection_failures_total[5m]) > 0.1
        for: 5m
        labels:
          severity: warning
        annotations:
          summary: "High rate of federation connection failures"
          description: "Connection failure rate is {{ $value }} per second"
      
      - alert: FederationPlacementFailures
        expr: rate(federation_placement_failures_total[5m]) > 0.05
        for: 5m
        labels:
          severity: warning
        annotations:
          summary: "Chunk placement failures detected"
          description: "Placement failure rate is {{ $value }} per second"
      
      - alert: FederationCircuitBreakerOpen
        expr: increase(federation_circuit_breaker_opens_total[5m]) > 3
        for: 1m
        labels:
          severity: warning
        annotations:
          summary: "Circuit breakers opening frequently"
          description: "{{ $value }} circuit breakers opened in last 5 minutes"
`
}