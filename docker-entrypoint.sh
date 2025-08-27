#!/bin/bash
set -e

# Auto-configuration for TLS certificates
if [ "$COLLECTIVE_AUTO_TLS" = "true" ]; then
    echo "🔐 Auto-configuring TLS certificates..."
    
    # Set default cert directory if not provided
    COLLECTIVE_CERT_DIR="${COLLECTIVE_CERT_DIR:-/collective/certs}"
    
    # Ensure required environment variables are set
    if [ -z "$COLLECTIVE_MEMBER_ID" ]; then
        echo "❌ Error: COLLECTIVE_MEMBER_ID is required for auto-TLS"
        exit 1
    fi
    
    # Determine component type from command if not set
    if [ -z "$COLLECTIVE_COMPONENT_TYPE" ] && [ "$1" = "coordinator" ]; then
        export COLLECTIVE_COMPONENT_TYPE="coordinator"
    elif [ -z "$COLLECTIVE_COMPONENT_TYPE" ] && [ "$1" = "node" ]; then
        export COLLECTIVE_COMPONENT_TYPE="node"
    fi
    
    # Auto-generate component ID if not set
    if [ -z "$COLLECTIVE_COMPONENT_ID" ]; then
        if [ "$COLLECTIVE_COMPONENT_TYPE" = "coordinator" ]; then
            export COLLECTIVE_COMPONENT_ID="${COLLECTIVE_MEMBER_ID}-coordinator"
        elif [ "$COLLECTIVE_COMPONENT_TYPE" = "node" ]; then
            # Use hostname for node ID or generate one
            HOSTNAME="${HOSTNAME:-$(hostname)}"
            export COLLECTIVE_COMPONENT_ID="${COLLECTIVE_MEMBER_ID}-${HOSTNAME}"
        fi
    fi
    
    echo "📋 Component Configuration:"
    echo "  Member ID: $COLLECTIVE_MEMBER_ID"
    echo "  Component Type: $COLLECTIVE_COMPONENT_TYPE"
    echo "  Component ID: $COLLECTIVE_COMPONENT_ID"
    echo "  Cert Directory: $COLLECTIVE_CERT_DIR"
    
    # Run auto-init to generate certificates
    /usr/local/bin/collective auth auto-init \
        --member-id="$COLLECTIVE_MEMBER_ID" \
        --component-type="$COLLECTIVE_COMPONENT_TYPE" \
        --component-id="$COLLECTIVE_COMPONENT_ID" \
        --cert-dir="$COLLECTIVE_CERT_DIR"
    
    # Export certificate paths for the application
    export COLLECTIVE_CA_CERT="$COLLECTIVE_CERT_DIR/ca/${COLLECTIVE_MEMBER_ID}-ca.crt"
    export COLLECTIVE_CERT="$COLLECTIVE_CERT_DIR/${COLLECTIVE_COMPONENT_TYPE}s/${COLLECTIVE_COMPONENT_ID}.crt"
    export COLLECTIVE_KEY="$COLLECTIVE_CERT_DIR/${COLLECTIVE_COMPONENT_TYPE}s/${COLLECTIVE_COMPONENT_ID}.key"
    
    echo "✅ TLS certificates configured successfully"
    
    # Create auth configuration file
    CONFIG_DIR="/tmp/collective-config"
    mkdir -p "$CONFIG_DIR"
    
    # Generate appropriate config based on component type
    if [ "$COLLECTIVE_COMPONENT_TYPE" = "coordinator" ]; then
        cat > "$CONFIG_DIR/config.json" <<EOF
{
  "mode": "coordinator",
  "member_id": "$COLLECTIVE_MEMBER_ID",
  "coordinator": {
    "address": ":8001",
    "data_dir": "/data/coordinator"
  },
  "auth": {
    "enabled": true,
    "ca_cert": "$COLLECTIVE_CA_CERT",
    "cert": "$COLLECTIVE_CERT",
    "key": "$COLLECTIVE_KEY",
    "client_ca": "$COLLECTIVE_CA_CERT",
    "peer_verification": true,
    "require_client_auth": true,
    "min_tls_version": "1.3"
  }
}
EOF
    elif [ "$COLLECTIVE_COMPONENT_TYPE" = "node" ]; then
        # Get coordinator address from environment or use default
        COORDINATOR_ADDRESS="${COLLECTIVE_COORDINATOR_ADDRESS:-${COLLECTIVE_MEMBER_ID}-coordinator:8001}"
        
        cat > "$CONFIG_DIR/config.json" <<EOF
{
  "mode": "node",
  "member_id": "$COLLECTIVE_MEMBER_ID",
  "node": {
    "node_id": "$COLLECTIVE_COMPONENT_ID",
    "address": ":9001",
    "data_dir": "/data/node",
    "storage_capacity": ${COLLECTIVE_STORAGE_CAPACITY:-10737418240},
    "coordinator_address": "$COORDINATOR_ADDRESS"
  },
  "auth": {
    "enabled": true,
    "ca_cert": "$COLLECTIVE_CA_CERT",
    "cert": "$COLLECTIVE_CERT",
    "key": "$COLLECTIVE_KEY",
    "min_tls_version": "1.3"
  }
}
EOF
    fi
    
    # Set config file path
    export COLLECTIVE_CONFIG_FILE="$CONFIG_DIR/config.json"
    echo "📄 Generated config: $COLLECTIVE_CONFIG_FILE"
fi

# If config file is set, use it
if [ -n "$COLLECTIVE_CONFIG_FILE" ]; then
    set -- "$@" --config "$COLLECTIVE_CONFIG_FILE"
fi

# Execute the collective binary with all arguments
exec /usr/local/bin/collective "$@"