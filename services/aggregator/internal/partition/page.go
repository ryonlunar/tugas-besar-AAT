package partition

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/ryonlunar/tugas-besar-AAT/contracts"
)

type Position struct {
	OccurredAt time.Time
	HazardID   string
}

// Reader queries one logical unit's current primary, after the supplied key,
// ordered by occurred_at DESC, hazard_id ASC, returning at most limit records.
type Reader struct {
	Unit string
	Read func(context.Context, contracts.Source, *Position, int) ([]contracts.HazardEvent, error)
}

type Query struct {
	Source contracts.Source
	Cursor string
	Limit  int
}

type Page struct {
	Items            []contracts.HazardEvent `json:"items"`
	NextCursor       *string                 `json:"next_cursor"`
	Partial          bool                    `json:"partial"`
	UnavailableUnits []string                `json:"unavailable_units"`
}

var ErrInvalidCursor = errors.New("invalid cursor or changed query scope")

type cursor struct {
	Version int
	Source  contracts.Source
	Units   string
	After   Position
}

// ReadPage is a bounded cross-unit read. Partial results have no continuation:
// restart from the first page after recovery, rather than skipping newer records
// from a unit that was unavailable. Complete scans use keyset pagination.
func ReadPage(ctx context.Context, readers []Reader, query Query, unitTimeout time.Duration) (Page, error) {
	source, token, limit := query.Source, query.Cursor, query.Limit
	page := Page{Items: []contracts.HazardEvent{}, UnavailableUnits: []string{}}
	if err := ctx.Err(); err != nil {
		return page, err
	}
	if len(readers) != UnitCount || limit < 1 || limit > 1000 || unitTimeout <= 0 || (source != "" && !source.Valid()) {
		return page, errors.New("invalid partition query")
	}
	names := make([]string, len(readers))
	for i, reader := range readers {
		if reader.Unit == "" || reader.Read == nil {
			return page, errors.New("unit name and reader required")
		}
		names[i] = reader.Unit
	}
	sort.Strings(names)
	for i := 1; i < len(names); i++ {
		if names[i] == names[i-1] {
			return page, errors.New("duplicate unit")
		}
	}
	scope := strings.Join(names, ",")
	if scope != "shard-0,shard-1,shard-2,shard-3" {
		return page, errors.New("all logical units must be declared")
	}
	var after *Position
	if token != "" {
		if len(token) > 4096 {
			return page, ErrInvalidCursor
		}
		raw, err := base64.RawURLEncoding.DecodeString(token)
		var parsed cursor
		if err != nil || json.Unmarshal(raw, &parsed) != nil || parsed.Version != 1 || parsed.Source != source || parsed.Units != scope || parsed.After.OccurredAt.IsZero() || parsed.After.HazardID == "" {
			return page, ErrInvalidCursor
		}
		after = &parsed.After
	}
	type result struct {
		unit   string
		events []contracts.HazardEvent
		err    error
	}
	results := make(chan result, len(readers))
	readCtx, cancel := context.WithTimeout(ctx, unitTimeout)
	defer cancel()
	pending := map[string]bool{}
	for _, reader := range readers {
		pending[reader.Unit] = true
		go func(reader Reader) {
			events, err := reader.Read(readCtx, source, after, limit+1)
			results <- result{reader.Unit, events, err}
		}(reader)
	}
	type entry struct {
		event contracts.HazardEvent
		at    time.Time
	}
	entries := []entry{}
	for len(pending) > 0 {
		select {
		case <-readCtx.Done():
			if ctx.Err() != nil {
				return page, ctx.Err()
			}
			for unit := range pending {
				page.UnavailableUnits = append(page.UnavailableUnits, unit)
				delete(pending, unit)
			}
		case result := <-results:
			delete(pending, result.unit)
			if result.err != nil {
				page.UnavailableUnits = append(page.UnavailableUnits, result.unit)
				continue
			}
			for _, event := range result.events {
				at, err := time.Parse(time.RFC3339Nano, event.OccurredAt)
				if err != nil {
					return page, errors.New("stored occurrence timestamp is invalid")
				}
				entries = append(entries, entry{event, at})
			}
		}
	}
	sort.Strings(page.UnavailableUnits)
	page.Partial = len(page.UnavailableUnits) > 0
	sort.Slice(entries, func(i, j int) bool {
		if !entries[i].at.Equal(entries[j].at) {
			return entries[i].at.After(entries[j].at)
		}
		return entries[i].event.HazardID < entries[j].event.HazardID
	})
	count := min(len(entries), limit)
	for _, item := range entries[:count] {
		page.Items = append(page.Items, item.event)
	}
	if !page.Partial && len(entries) > limit {
		last := entries[count-1]
		raw, _ := json.Marshal(cursor{1, source, scope, Position{last.at, last.event.HazardID}})
		next := base64.RawURLEncoding.EncodeToString(raw)
		page.NextCursor = &next
	}
	return page, nil
}
