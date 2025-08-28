#!/bin/bash
set -e

# Script to generate federation CA hierarchy for Collective Storage
# Usage: ./generate_federation_ca.sh [federation-domain] [member1] [member2] [member3]

FEDERATION_DOMAIN=${1:-"collective.local"}
MEMBER1=${2:-"alice.$FEDERATION_DOMAIN"}
MEMBER2=${3:-"bob.$FEDERATION_DOMAIN"}
MEMBER3=${4:-"carol.$FEDERATION_DOMAIN"}

OUTPUT_DIR="federation-ca"
BIN_PATH="./bin/collective"

echo "========================================="
echo "Collective Storage Federation CA Setup"
echo "========================================="
echo "Federation Domain: $FEDERATION_DOMAIN"
echo "Members: $MEMBER1, $MEMBER2, $MEMBER3"
echo ""

# Build the binary if it doesn't exist
if [ ! -f "$BIN_PATH" ]; then
    echo "Building collective binary..."
    go build -o bin/collective ./cmd/collective/
fi

# Step 1: Generate Root CA
echo "Step 1: Generating Federation Root CA..."
$BIN_PATH federation ca init \
    --domain "$FEDERATION_DOMAIN" \
    --output "$OUTPUT_DIR" \
    --validity-years 5

echo "✓ Root CA created at $OUTPUT_DIR/"
echo ""

# Step 2: Generate Member CAs
echo "Step 2: Generating Member Intermediate CAs..."

# Member 1
echo "  Generating CA for $MEMBER1..."
$BIN_PATH federation ca sign-member \
    --member "$MEMBER1" \
    --root-ca "$OUTPUT_DIR" \
    --output "$OUTPUT_DIR/${MEMBER1}-ca.crt" \
    --validity-years 3

# Member 2
echo "  Generating CA for $MEMBER2..."
$BIN_PATH federation ca sign-member \
    --member "$MEMBER2" \
    --root-ca "$OUTPUT_DIR" \
    --output "$OUTPUT_DIR/${MEMBER2}-ca.crt" \
    --validity-years 3

# Member 3
echo "  Generating CA for $MEMBER3..."
$BIN_PATH federation ca sign-member \
    --member "$MEMBER3" \
    --root-ca "$OUTPUT_DIR" \
    --output "$OUTPUT_DIR/${MEMBER3}-ca.crt" \
    --validity-years 3

echo ""
echo "Step 3: Verifying Certificate Chain..."
echo ""

# Verify all certificates
for member in "$MEMBER1" "$MEMBER2" "$MEMBER3"; do
    echo -n "  Verifying $member: "
    openssl verify -CAfile "$OUTPUT_DIR/ca.crt" "$OUTPUT_DIR/${member}-ca.crt" 2>/dev/null
done

echo ""
echo "========================================="
echo "Federation CA Setup Complete!"
echo "========================================="
echo ""
echo "Generated files:"
echo "  Root CA Certificate: $OUTPUT_DIR/ca.crt"
echo "  Root CA Private Key: $OUTPUT_DIR/ca.key"
echo "  Member CAs:"
echo "    - $OUTPUT_DIR/${MEMBER1}-ca.crt (.key)"
echo "    - $OUTPUT_DIR/${MEMBER2}-ca.crt (.key)"
echo "    - $OUTPUT_DIR/${MEMBER3}-ca.crt (.key)"
echo ""
echo "Next steps:"
echo "  1. Distribute root CA certificate to all members"
echo "  2. Each member uses their intermediate CA to sign node/client certificates"
echo "  3. Configure trust stores to include root + all member CAs"
echo ""