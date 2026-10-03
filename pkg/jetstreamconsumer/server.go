package jetstreamconsumer

import (
	"context"
	"errors"
	"net/http"
	"time"
)

// Serve joins the consumer before returning, so its owner can safely close storage.
func Serve(ctx context.Context, run func(context.Context) error, server *http.Server) error {
	workerCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	workerDone := make(chan error, 1)
	go func() { workerDone <- run(workerCtx) }()
	serverDone := make(chan error, 1)
	go func() { serverDone <- server.ListenAndServe() }()
	var result error
	workerFinished := false
	select {
	case result = <-workerDone:
		workerFinished = true
	case result = <-serverDone:
		if errors.Is(result, http.ErrServerClosed) {
			result = nil
		}
	case <-ctx.Done():
	}
	cancel()
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		result = errors.Join(result, err, server.Close())
	}
	if !workerFinished {
		result = errors.Join(result, <-workerDone)
	}
	return result
}
