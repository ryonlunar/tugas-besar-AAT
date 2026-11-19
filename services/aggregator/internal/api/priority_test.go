package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPriorityRequestsUseReservedCapacity(t *testing.T) {
	general, reserved := SplitCapacity(4)
	if general != 3 || reserved != 1 {
		t.Fatalf("split=%d/%d", general, reserved)
	}
	h := NewHandlerWithLimit(nil, "key", nil, 4)
	entered, release := make(chan struct{}), make(chan struct{})
	blocking := h.admit(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		entered <- struct{}{}
		<-release
		w.WriteHeader(http.StatusOK)
	}))
	done := make(chan int, general+1)
	request := func(priority bool) {
		req := httptest.NewRequest(http.MethodGet, "/internal/v1/hazards", nil).WithContext(context.Background())
		if priority {
			req.Header.Set("X-Request-Priority", "high")
		}
		res := httptest.NewRecorder()
		blocking.ServeHTTP(res, req)
		done <- res.Code
	}
	for i := 0; i < general; i++ {
		go request(false)
		<-entered
	}
	go request(false)
	if code := <-done; code != http.StatusTooManyRequests {
		t.Fatalf("ordinary request beyond general capacity status=%d", code)
	}
	go request(true)
	<-entered
	close(release)
	for i := 0; i < general+1; i++ {
		if code := <-done; code != http.StatusOK {
			t.Fatalf("admitted request status=%d", code)
		}
	}
}
