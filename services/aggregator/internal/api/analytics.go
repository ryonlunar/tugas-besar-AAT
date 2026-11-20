package api

import (
	"context"
	"net/http"
	"time"

	"github.com/ryonlunar/tugas-besar-AAT/services/aggregator/internal/sharded"
)

type analyticsRepository interface {
	Analytics(context.Context, time.Time, time.Time, string) sharded.Analytics
}

func (h *Handler) analytics(w http.ResponseWriter, r *http.Request) {
	if !h.authorized(w, r) {
		return
	}
	repo, ok := h.repository.(analyticsRepository)
	if !ok {
		writeError(w, 503, "ANALYTICS_UNAVAILABLE", "Analytics requires replicated storage", correlationID(r))
		return
	}
	q := r.URL.Query()
	from, e1 := time.Parse(time.RFC3339, q.Get("from"))
	to, e2 := time.Parse(time.RFC3339, q.Get("to"))
	if e1 != nil || e2 != nil || len(q) != 2 || len(q["from"]) != 1 || len(q["to"]) != 1 || !from.Before(to) || to.Sub(from) > 366*24*time.Hour {
		writeError(w, 400, "INVALID_QUERY", "Provide from and to timestamps within a 366 day range", correlationID(r))
		return
	}
	step := "1 hour"
	if to.Sub(from) > 90*24*time.Hour {
		step = "7 days"
	} else if to.Sub(from) > 3*24*time.Hour {
		step = "1 day"
	}
	writeJSON(w, 200, struct {
		sharded.Analytics
		Step string `json:"step"`
	}{repo.Analytics(r.Context(), from, to, step), step})
}
