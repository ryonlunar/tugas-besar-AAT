package api

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"sort"
	"sync"
	"time"
)

// backendPool caches headless-Service DNS answers. Reads try distinct replicas
// within one deadline; overload responses are returned without retrying them.
type backendPool struct {
	mu        sync.Mutex
	host      string
	lookup    func(context.Context, string) ([]string, error)
	addresses []string
	expires   time.Time
	failed    map[string]time.Time
	next      uint64
}

func (p *backendPool) candidates(ctx context.Context) ([]string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := time.Now()
	if !now.Before(p.expires) {
		lookupCtx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
		addresses, err := p.lookup(lookupCtx, p.host)
		cancel()
		if err == nil && len(addresses) > 0 {
			unique := map[string]bool{}
			filtered := []string{}
			failed := map[string]time.Time{}
			for _, address := range addresses {
				if net.ParseIP(address) != nil && !unique[address] {
					unique[address] = true
					filtered = append(filtered, address)
					failed[address] = p.failed[address]
				}
			}
			if len(filtered) > 0 {
				sort.Strings(filtered)
				p.addresses = filtered
				p.failed = failed
			}
		}
		if len(p.addresses) == 0 {
			return nil, errors.New("no Aggregator replicas available")
		}
		p.expires = now.Add(5 * time.Second)
	}
	ordered := []string{}
	for i := range p.addresses {
		address := p.addresses[(int(p.next%uint64(len(p.addresses)))+i)%len(p.addresses)]
		if !now.Before(p.failed[address]) {
			ordered = append(ordered, address)
		}
	}
	p.next++
	if len(ordered) == 0 {
		ordered = append(ordered, p.addresses...)
	}
	return ordered, nil
}

func (p *backendPool) unavailable(address string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.failed == nil {
		p.failed = map[string]time.Time{}
	}
	p.failed[address] = time.Now().Add(2 * time.Second)
}

type cancelBody struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (b *cancelBody) Close() error { err := b.ReadCloser.Close(); b.cancel(); return err }

func (h *Handler) readUpstream(request *http.Request) (*http.Response, error) {
	// GETs and requests carrying an Idempotency-Key are safe to repeat on
	// another replica: Aggregator returns the first receipt for a repeated key.
	if h.discovery == nil || (request.Method != http.MethodGet && request.Header.Get("Idempotency-Key") == "") {
		return h.client.Do(request)
	}
	ctx, cancel := context.WithTimeout(request.Context(), h.cfg.Timeout)
	// The successful body's Close owns cancellation; failed attempts cancel here.
	complete := false
	defer func() {
		if !complete {
			cancel()
		}
	}()
	addresses, err := h.discovery.candidates(ctx)
	if err != nil {
		return nil, err
	}
	port := request.URL.Port()
	if port == "" {
		port = "80"
	}
	attempts := min(len(addresses), 3)
	for i, address := range addresses[:attempts] {
		deadline, _ := ctx.Deadline()
		// Readiness has a shorter parent deadline than ordinary requests. Divide
		// the actual remaining budget so one frozen peer cannot consume it all.
		budget := time.Until(deadline) / time.Duration(attempts-i)
		if budget <= 0 {
			return nil, context.DeadlineExceeded
		}
		attemptCtx, stop := context.WithTimeout(ctx, budget)
		attempt := request.Clone(attemptCtx)
		if request.GetBody != nil {
			if attempt.Body, err = request.GetBody(); err != nil {
				stop()
				return nil, err
			}
		}
		attempt.URL.Host = net.JoinHostPort(address, port)
		attempt.Host = request.URL.Host
		response, attemptErr := h.client.Do(attempt)
		if attemptErr == nil {
			response.Body = &cancelBody{ReadCloser: response.Body, cancel: func() { stop(); cancel() }}
			complete = true
			return response, nil
		}
		stop()
		err = attemptErr
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		h.discovery.unavailable(address)
	}
	return nil, err
}
