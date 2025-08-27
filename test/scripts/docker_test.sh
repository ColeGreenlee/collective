#!/bin/bash

# Docker-based Integration Test Suite for Collective
# Tests node resilience and self-healing capabilities

set -e

echo "🧪 COLLECTIVE DOCKER TEST SUITE"
echo "================================"

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# Test results
TESTS_PASSED=0
TESTS_FAILED=0

# Helper functions
log_info() {
    echo -e "${GREEN}ℹ️  $1${NC}"
}

log_warn() {
    echo -e "${YELLOW}⚠️  $1${NC}"
}

log_error() {
    echo -e "${RED}❌ $1${NC}"
}

log_success() {
    echo -e "${GREEN}✅ $1${NC}"
}

# Build the Docker image
build_image() {
    log_info "Building Docker image..."
    docker compose build --quiet
    log_success "Docker image built"
}

# Start the cluster
start_cluster() {
    log_info "Starting collective cluster..."
    docker compose up -d --build
    sleep 5  # Wait for services to start
    log_success "Cluster started"
}

# Stop the cluster
stop_cluster() {
    log_info "Stopping collective cluster..."
    docker compose down -v
    log_success "Cluster stopped"
}

# Test 1: Verify cluster formation
test_cluster_formation() {
    log_info "Test 1: Cluster Formation"
    
    # Check if all coordinators are running
    COORDINATOR_COUNT=$(docker compose ps | grep coordinator | grep Up | wc -l)
    if [ "$COORDINATOR_COUNT" -eq 3 ]; then
        log_success "All 3 coordinators are running"
        ((TESTS_PASSED++))
    else
        log_error "Expected 3 coordinators, found $COORDINATOR_COUNT"
        ((TESTS_FAILED++))
        return 1
    fi
    
    # Check if all nodes are running
    NODE_COUNT=$(docker compose ps | grep node | grep Up | wc -l)
    if [ "$NODE_COUNT" -eq 15 ]; then
        log_success "All 15 storage nodes are running"
        ((TESTS_PASSED++))
    else
        log_error "Expected 15 nodes, found $NODE_COUNT"
        ((TESTS_FAILED++))
        return 1
    fi
    
    # Check cluster status via CLI
    ./bin/collective status --coordinator alice:8001 --json > /tmp/cluster_status.json 2>/dev/null
    
    TOTAL_NODES=$(jq '.local_nodes | length' /tmp/cluster_status.json)
    if [ "$TOTAL_NODES" -ge 5 ]; then
        log_success "Coordinator reports $TOTAL_NODES nodes registered"
        ((TESTS_PASSED++))
    else
        log_error "Coordinator reports only $TOTAL_NODES nodes"
        ((TESTS_FAILED++))
    fi
}

# Test 2: Node resilience after coordinator restart
test_coordinator_restart() {
    log_info "Test 2: Coordinator Restart Resilience"
    
    # Get initial node count
    INITIAL_NODES=$(./bin/collective status --coordinator alice:8001 --json 2>/dev/null | jq '.local_nodes | length')
    log_info "Initial nodes registered: $INITIAL_NODES"
    
    # Restart alice coordinator
    log_warn "Restarting alice coordinator..."
    docker compose restart alice
    sleep 3
    
    # Wait for heartbeat cycle (15 seconds)
    log_info "Waiting for nodes to re-register (15 seconds)..."
    sleep 15
    
    # Check if nodes re-registered
    FINAL_NODES=$(./bin/collective status --coordinator alice:8001 --json 2>/dev/null | jq '.local_nodes | length')
    
    if [ "$FINAL_NODES" -eq "$INITIAL_NODES" ]; then
        log_success "All $FINAL_NODES nodes successfully re-registered after coordinator restart"
        ((TESTS_PASSED++))
    else
        log_error "Only $FINAL_NODES of $INITIAL_NODES nodes re-registered"
        ((TESTS_FAILED++))
    fi
}

# Test 3: Data persistence through node restart
test_node_restart() {
    log_info "Test 3: Node Restart Data Persistence"
    
    # Create test file
    TEST_FILE="/tmp/test_resilience_$(date +%s).txt"
    echo "Test data for resilience testing" > "$TEST_FILE"
    
    # Store file
    ./bin/collective client store --file "$TEST_FILE" --id "/test/resilience.txt" --coordinator alice:8001
    if [ $? -eq 0 ]; then
        log_success "File stored successfully"
        ((TESTS_PASSED++))
    else
        log_error "Failed to store file"
        ((TESTS_FAILED++))
        return 1
    fi
    
    # Restart a storage node
    log_warn "Restarting alice-node-01..."
    docker compose restart alice-node-01
    sleep 5
    
    # Try to retrieve file
    RETRIEVED_FILE="/tmp/retrieved_$(date +%s).txt"
    ./bin/collective client retrieve --id "/test/resilience.txt" --output "$RETRIEVED_FILE" --coordinator alice:8001
    
    if [ $? -eq 0 ] && cmp -s "$TEST_FILE" "$RETRIEVED_FILE"; then
        log_success "File retrieved successfully after node restart"
        ((TESTS_PASSED++))
    else
        log_error "Failed to retrieve file or data mismatch"
        ((TESTS_FAILED++))
    fi
    
    # Cleanup
    rm -f "$TEST_FILE" "$RETRIEVED_FILE"
}

# Test 4: Multiple node failures
test_multiple_node_failures() {
    log_info "Test 4: Multiple Node Failure Resilience"
    
    # Stop 2 nodes from alice
    log_warn "Stopping alice-node-01 and alice-node-02..."
    docker compose stop alice-node-01 alice-node-02
    sleep 5
    
    # Check cluster is still functional
    STATUS=$(./bin/collective status --coordinator alice:8001 --json 2>/dev/null | jq -r '.member_id')
    
    if [ "$STATUS" = "alice" ]; then
        log_success "Cluster still operational with 2 nodes down"
        ((TESTS_PASSED++))
    else
        log_error "Cluster not responding properly"
        ((TESTS_FAILED++))
    fi
    
    # Restart stopped nodes
    log_info "Restarting stopped nodes..."
    docker compose start alice-node-01 alice-node-02
    sleep 15  # Wait for re-registration
    
    # Verify nodes re-registered
    NODE_COUNT=$(./bin/collective status --coordinator alice:8001 --json 2>/dev/null | jq '.local_nodes | length')
    if [ "$NODE_COUNT" -eq 5 ]; then
        log_success "All nodes re-registered after restart"
        ((TESTS_PASSED++))
    else
        log_error "Only $NODE_COUNT nodes registered, expected 5"
        ((TESTS_FAILED++))
    fi
}

# Test 5: Concurrent operations during failures
test_concurrent_operations() {
    log_info "Test 5: Concurrent Operations During Failures"
    
    # Start background writes
    for i in {1..5}; do
        (
            TEST_FILE="/tmp/concurrent_test_$i.txt"
            echo "Concurrent test data $i" > "$TEST_FILE"
            ./bin/collective client store --file "$TEST_FILE" --id "/test/concurrent_$i.txt" --coordinator alice:8001 2>/dev/null
            rm -f "$TEST_FILE"
        ) &
    done
    
    # Restart a node while operations are running
    sleep 1
    docker compose restart alice-node-03 2>/dev/null
    
    # Wait for operations to complete
    wait
    
    # Verify at least some operations succeeded
    SUCCESS_COUNT=0
    for i in {1..5}; do
        if ./bin/collective ls /test --coordinator alice:8001 2>/dev/null | grep -q "concurrent_$i.txt"; then
            ((SUCCESS_COUNT++))
        fi
    done
    
    if [ "$SUCCESS_COUNT" -ge 3 ]; then
        log_success "$SUCCESS_COUNT/5 concurrent operations succeeded during node restart"
        ((TESTS_PASSED++))
    else
        log_error "Only $SUCCESS_COUNT/5 concurrent operations succeeded"
        ((TESTS_FAILED++))
    fi
}

# Main test execution
main() {
    echo ""
    log_info "Starting Docker-based integration tests..."
    echo ""
    
    # Ensure we're in the project root
    if [ ! -f "docker-compose.yml" ]; then
        log_error "docker-compose.yml not found. Please run from project root."
        exit 1
    fi
    
    # Build collective binary
    log_info "Building collective binary..."
    go build -o bin/collective ./cmd/collective
    
    # Start with clean slate
    stop_cluster 2>/dev/null || true
    
    # Start cluster
    start_cluster
    
    # Run tests
    echo ""
    echo "Running Test Suite:"
    echo "==================="
    
    test_cluster_formation
    echo ""
    
    test_coordinator_restart
    echo ""
    
    test_node_restart
    echo ""
    
    test_multiple_node_failures
    echo ""
    
    test_concurrent_operations
    echo ""
    
    # Cleanup
    stop_cluster
    
    # Report results
    echo ""
    echo "================================"
    echo "TEST RESULTS"
    echo "================================"
    echo -e "${GREEN}Passed: $TESTS_PASSED${NC}"
    echo -e "${RED}Failed: $TESTS_FAILED${NC}"
    echo ""
    
    if [ "$TESTS_FAILED" -eq 0 ]; then
        log_success "All tests passed! 🎉"
        exit 0
    else
        log_error "Some tests failed. Please review the output above."
        exit 1
    fi
}

# Run main function
main "$@"