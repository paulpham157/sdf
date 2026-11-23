package server

import (
	"context"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type stubStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s stubStream) Context() context.Context { return s.ctx }

func TestStreamAuthInterceptor(t *testing.T) {
	s := &Server{Token: "test-otlp-token"}
	handlerRan := false
	handler := func(any, grpc.ServerStream) error {
		handlerRan = true
		return nil
	}

	okCtx := metadata.NewIncomingContext(t.Context(), metadata.MD{
		"authorization": []string{"Bearer test-otlp-token"},
	})
	if err := s.streamAuthInterceptor(nil, stubStream{ctx: okCtx}, nil, handler); err != nil {
		t.Fatalf("correct stream %v", err)
	}
	if !handlerRan {
		t.Fatal("handler did not run")
	}

	handlerRan = false
	multiCtx := metadata.NewIncomingContext(t.Context(), metadata.MD{
		"authorization": []string{"Bearer test-otlp-token", "Bearer test-otlp-token"},
	})
	err := s.streamAuthInterceptor(nil, stubStream{ctx: multiCtx}, nil, handler)
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("multiple authorization values %v", err)
	}
	if handlerRan {
		t.Fatal("handler ran for multiple authorization values")
	}
}
