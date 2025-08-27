package auth

import (
	"context"
	"fmt"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

// ContextKey for storing identity in context
type contextKey string

const (
	IdentityContextKey contextKey = "identity"
	TokenMetadataKey   string     = "authorization"
)

// AuthInterceptor provides gRPC authentication interceptors
type AuthInterceptor struct {
	authenticator Authenticator
	tokenManager  TokenManager
	requireAuth   bool
}

// NewAuthInterceptor creates a new authentication interceptor
func NewAuthInterceptor(authenticator Authenticator, tokenManager TokenManager, requireAuth bool) *AuthInterceptor {
	return &AuthInterceptor{
		authenticator: authenticator,
		tokenManager:  tokenManager,
		requireAuth:   requireAuth,
	}
}

// UnaryServerInterceptor returns a gRPC unary server interceptor for authentication
func (ai *AuthInterceptor) UnaryServerInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
		// Extract and validate identity
		newCtx, err := ai.authenticate(ctx)
		if err != nil {
			if ai.requireAuth {
				return nil, status.Errorf(codes.Unauthenticated, "authentication failed: %v", err)
			}
			// If auth not required, continue without identity
			newCtx = ctx
		}

		// Call the handler with authenticated context
		return handler(newCtx, req)
	}
}

// StreamServerInterceptor returns a gRPC stream server interceptor for authentication
func (ai *AuthInterceptor) StreamServerInterceptor() grpc.StreamServerInterceptor {
	return func(srv interface{}, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		// Extract and validate identity
		newCtx, err := ai.authenticate(ss.Context())
		if err != nil {
			if ai.requireAuth {
				return status.Errorf(codes.Unauthenticated, "authentication failed: %v", err)
			}
			// If auth not required, continue without identity
			newCtx = ss.Context()
		}

		// Wrap the stream with authenticated context
		wrappedStream := &authenticatedServerStream{
			ServerStream: ss,
			ctx:          newCtx,
		}

		// Call the handler with wrapped stream
		return handler(srv, wrappedStream)
	}
}

// UnaryClientInterceptor returns a gRPC unary client interceptor for authentication
func (ai *AuthInterceptor) UnaryClientInterceptor(token string) grpc.UnaryClientInterceptor {
	return func(ctx context.Context, method string, req, reply interface{}, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		// Add token to metadata if provided
		if token != "" {
			ctx = metadata.AppendToOutgoingContext(ctx, TokenMetadataKey, fmt.Sprintf("Bearer %s", token))
		}

		// Invoke the RPC
		return invoker(ctx, method, req, reply, cc, opts...)
	}
}

// StreamClientInterceptor returns a gRPC stream client interceptor for authentication
func (ai *AuthInterceptor) StreamClientInterceptor(token string) grpc.StreamClientInterceptor {
	return func(ctx context.Context, desc *grpc.StreamDesc, cc *grpc.ClientConn, method string, streamer grpc.Streamer, opts ...grpc.CallOption) (grpc.ClientStream, error) {
		// Add token to metadata if provided
		if token != "" {
			ctx = metadata.AppendToOutgoingContext(ctx, TokenMetadataKey, fmt.Sprintf("Bearer %s", token))
		}

		// Create the stream
		return streamer(ctx, desc, cc, method, opts...)
	}
}

// authenticate extracts and validates identity from the request context
func (ai *AuthInterceptor) authenticate(ctx context.Context) (context.Context, error) {
	// Try to authenticate via TLS certificate first
	if identity, err := ai.authenticateFromTLS(ctx); err == nil {
		return context.WithValue(ctx, IdentityContextKey, identity), nil
	}

	// Try to authenticate via token
	if ai.tokenManager != nil {
		if identity, err := ai.authenticateFromToken(ctx); err == nil {
			return context.WithValue(ctx, IdentityContextKey, identity), nil
		}
	}

	return ctx, fmt.Errorf("no valid authentication method found")
}

// authenticateFromTLS extracts identity from TLS certificate
func (ai *AuthInterceptor) authenticateFromTLS(ctx context.Context) (*Identity, error) {
	// Extract peer info from context
	p, ok := peer.FromContext(ctx)
	if !ok {
		return nil, fmt.Errorf("no peer info in context")
	}

	// Check if TLS is enabled
	tlsInfo, ok := p.AuthInfo.(credentials.TLSInfo)
	if !ok {
		return nil, fmt.Errorf("no TLS info in context")
	}

	// Verify we have peer certificates
	if len(tlsInfo.State.PeerCertificates) == 0 {
		return nil, fmt.Errorf("no peer certificates")
	}

	// Extract identity from certificate directly if no authenticator
	cert := tlsInfo.State.PeerCertificates[0]

	if ai.authenticator != nil {
		// Use authenticator if available
		identity, err := ai.authenticator.GetIdentity(cert)
		if err != nil {
			return nil, fmt.Errorf("failed to get identity from certificate: %w", err)
		}

		// Validate the identity
		if err := ai.authenticator.ValidateIdentity(ctx, identity); err != nil {
			return nil, fmt.Errorf("identity validation failed: %w", err)
		}

		return identity, nil
	}

	// Extract identity manually from certificate
	certManager := &CertManager{}
	identity, err := certManager.GetIdentityFromCert(cert)
	if err != nil {
		return nil, fmt.Errorf("failed to extract identity: %w", err)
	}

	return identity, nil
}

// authenticateFromToken extracts identity from authorization token
func (ai *AuthInterceptor) authenticateFromToken(ctx context.Context) (*Identity, error) {
	// Extract metadata from context
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return nil, fmt.Errorf("no metadata in context")
	}

	// Get authorization header
	authHeaders := md.Get(TokenMetadataKey)
	if len(authHeaders) == 0 {
		return nil, fmt.Errorf("no authorization header")
	}

	// Parse Bearer token
	var token string
	if _, err := fmt.Sscanf(authHeaders[0], "Bearer %s", &token); err != nil {
		return nil, fmt.Errorf("invalid authorization header format")
	}

	// Validate token and get identity
	identity, err := ai.tokenManager.ValidateToken(token)
	if err != nil {
		return nil, fmt.Errorf("token validation failed: %w", err)
	}

	return identity, nil
}

// GetIdentityFromContext retrieves the identity from context
func GetIdentityFromContext(ctx context.Context) (*Identity, bool) {
	identity, ok := ctx.Value(IdentityContextKey).(*Identity)
	return identity, ok
}

// authenticatedServerStream wraps a ServerStream with authenticated context
type authenticatedServerStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s *authenticatedServerStream) Context() context.Context {
	return s.ctx
}

// RequireComponentType returns an interceptor that requires a specific component type
func RequireComponentType(componentType ComponentType) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
		identity, ok := GetIdentityFromContext(ctx)
		if !ok {
			return nil, status.Error(codes.Unauthenticated, "no identity in context")
		}

		if identity.Type != componentType {
			return nil, status.Errorf(codes.PermissionDenied, "requires %s component, got %s", componentType, identity.Type)
		}

		return handler(ctx, req)
	}
}

// RequireMemberID returns an interceptor that requires a specific member ID
func RequireMemberID(memberID string) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
		identity, ok := GetIdentityFromContext(ctx)
		if !ok {
			return nil, status.Error(codes.Unauthenticated, "no identity in context")
		}

		if identity.MemberID != memberID {
			return nil, status.Errorf(codes.PermissionDenied, "requires member %s, got %s", memberID, identity.MemberID)
		}

		return handler(ctx, req)
	}
}
