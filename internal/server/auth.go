package server

import (
	"context"
	"crypto/subtle"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// writePaths are the HTTP routes that store telemetry or session events.
var writePaths = map[string]struct{}{
	"/v1/traces":   {},
	"/v1/metrics":  {},
	"/v1/logs":     {},
	"/v1/sessions": {},
}

func (s *Server) requiresAuth(method, path string) bool {
	if s.Token == "" {
		return false
	}
	if method != "POST" {
		return false
	}
	_, ok := writePaths[path]
	return ok
}

func (s *Server) authorizedBearer(raw string) bool {
	if s.Token == "" {
		return true
	}
	return bearerMatches(raw, s.Token)
}

func bearerMatches(raw, token string) bool {
	scheme, param, ok := cutScheme(raw)
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		constantTimeEqual(token, token)
		return false
	}
	return constantTimeEqual(param, token)
}

func cutScheme(raw string) (scheme, param string, ok bool) {
	raw = strings.TrimSpace(raw)
	i := strings.IndexAny(raw, " \t")
	if i < 0 {
		return raw, "", false
	}
	return raw[:i], strings.TrimSpace(raw[i+1:]), true
}

func constantTimeEqual(a, b string) bool {
	ab, bb := []byte(a), []byte(b)
	if len(ab) != len(bb) {
		constantTimeEqual(b, b)
		return false
	}
	return subtle.ConstantTimeCompare(ab, bb) == 1
}

func (s *Server) authInterceptor(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
	if err := s.checkGRPCAuth(ctx); err != nil {
		return nil, err
	}
	return handler(ctx, req)
}

func (s *Server) checkGRPCAuth(ctx context.Context) error {
	if s.Token == "" {
		return nil
	}
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return status.Error(codes.Unauthenticated, "unauthenticated")
	}
	values := md.Get("authorization")
	if len(values) == 0 || !s.authorizedBearer(values[0]) {
		return status.Error(codes.Unauthenticated, "unauthenticated")
	}
	return nil
}
