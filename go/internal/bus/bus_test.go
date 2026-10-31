package bus_test

import (
	"context"
	"testing"
	"time"

	"github.com/paulpham157/sdf/go/internal/bus"
)

func TestPublishBackpressureBlocks(t *testing.T) {
	b := bus.New(1)
	defer b.Close()
	ch := b.Subscribe("s")

	ctx := context.Background()
	if err := b.Publish(ctx, bus.Message{Payload: []byte("1")}); err != nil {
		t.Fatal(err)
	}
	// Buffer full: Publish should block until we receive or ctx cancels.
	ctx2, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	err := b.Publish(ctx2, bus.Message{Payload: []byte("2")})
	if err == nil {
		t.Fatal("expected block/cancel when buffer full")
	}

	<-ch // free one slot
	ctx3, cancel3 := context.WithTimeout(context.Background(), time.Second)
	defer cancel3()
	if err := b.Publish(ctx3, bus.Message{Payload: []byte("3")}); err != nil {
		t.Fatal(err)
	}
}
