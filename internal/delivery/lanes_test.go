package delivery

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/vm75/message-sync/internal/transport"
)

func TestLanesAreIndependentAndFIFO(t *testing.T) {
	manager, err := New(context.Background(), 4, []transport.EndpointID{"slow", "fast"})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()

	started := make(chan struct{})
	release := make(chan struct{})
	if err := manager.Enqueue(context.Background(), "slow", func(context.Context) {
		close(started)
		<-release
	}); err != nil {
		t.Fatal(err)
	}
	<-started
	var mu sync.Mutex
	var order []int
	for _, value := range []int{1, 2, 3} {
		value := value
		if err := manager.Enqueue(context.Background(), "fast", func(context.Context) {
			mu.Lock()
			order = append(order, value)
			mu.Unlock()
		}); err != nil {
			t.Fatal(err)
		}
	}
	deadline := time.After(time.Second)
	for {
		mu.Lock()
		complete := len(order) == 3
		mu.Unlock()
		if complete {
			break
		}
		select {
		case <-deadline:
			t.Fatal("healthy lane was blocked by slow lane")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	mu.Lock()
	if want := []int{1, 2, 3}; len(order) != len(want) || order[0] != want[0] || order[1] != want[1] || order[2] != want[2] {
		t.Fatalf("order = %v, want %v", order, want)
	}
	mu.Unlock()
	close(release)
}

func TestFullLaneIsExplicitAndReloadRemovesEndpoint(t *testing.T) {
	manager, err := New(context.Background(), 1, []transport.EndpointID{"one"})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	block := make(chan struct{})
	started := make(chan struct{})
	if err := manager.Enqueue(context.Background(), "one", func(context.Context) {
		close(started)
		<-block
	}); err != nil {
		t.Fatal(err)
	}
	<-started
	if err := manager.Enqueue(context.Background(), "one", func(context.Context) {}); err != nil {
		t.Fatal(err)
	}
	if err := manager.Enqueue(context.Background(), "one", func(context.Context) {}); !errors.Is(err, ErrFull) {
		t.Fatalf("full enqueue error = %v, want ErrFull", err)
	}
	close(block)
	if err := manager.Update(nil); err != nil {
		t.Fatal(err)
	}
	if err := manager.Enqueue(context.Background(), "one", func(context.Context) {}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("removed endpoint error = %v, want ErrUnavailable", err)
	}
}
