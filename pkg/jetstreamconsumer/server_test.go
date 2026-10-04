package jetstreamconsumer

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"
)

func TestServeWaitsForWorkerBeforeReturning(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cancelled := make(chan struct{})
	release := make(chan struct{})
	workerError := errors.New("worker shutdown error")
	done := make(chan error, 1)
	go func() {
		done <- Serve(ctx, func(ctx context.Context) error {
			<-ctx.Done()
			close(cancelled)
			<-release
			return workerError
		}, &http.Server{Addr: "127.0.0.1:0"})
	}()
	cancel()
	<-cancelled
	select {
	case err := <-done:
		t.Fatalf("returned before worker finished: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	select {
	case err := <-done:
		if !errors.Is(err, workerError) {
			t.Fatalf("worker failure was lost: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("server did not finish shutdown")
	}
}
