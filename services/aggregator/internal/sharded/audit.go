package sharded

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"sort"
	"strings"
	"time"

	"github.com/ryonlunar/tugas-besar-AAT/contracts"
	"github.com/ryonlunar/tugas-besar-AAT/services/aggregator/internal/audit"
	"github.com/ryonlunar/tugas-besar-AAT/services/aggregator/internal/partition"
)

type auditCursor struct {
	Version  int
	Scope    string
	At       time.Time
	ID       string
	Source   contracts.Source
	Sequence int64
}

func cursorFor(q audit.Query, endpoint string) (auditCursor, error) {
	scope := endpoint + ":" + string(q.Source) + ":" + q.Since.UTC().Format(time.RFC3339Nano)
	c := auditCursor{Version: 1, Scope: scope}
	if (q.Source != "" && !q.Source.Valid()) || q.Limit < 1 || q.Limit > 1000 {
		return c, audit.ErrQuery
	}
	if q.Cursor != "" {
		if len(q.Cursor) > 4096 {
			return c, audit.ErrQuery
		}
		raw, err := base64.RawURLEncoding.DecodeString(q.Cursor)
		if err != nil || json.Unmarshal(raw, &c) != nil || c.Version != 1 || c.Scope != scope {
			return c, audit.ErrQuery
		}
	}
	return c, nil
}
func encodeCursor(c auditCursor) *string {
	raw, _ := json.Marshal(c)
	s := base64.RawURLEncoding.EncodeToString(raw)
	return &s
}

func fanout[T any](ctx context.Context, s *Store, read func(context.Context, unit) (T, error)) (map[string]T, []string) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	type result struct {
		name  string
		value T
		err   error
	}
	ch := make(chan result, len(s.units))
	pending := map[string]bool{}
	for _, u := range s.units {
		pending[u.name] = true
		go func(u unit) {
			readCtx, span := otel.Tracer("aat/audit").Start(ctx, "postgres.audit")
			span.SetAttributes(attribute.String("unit", u.name))
			v, err := read(readCtx, u)
			if err != nil {
				span.SetStatus(codes.Error, "audit unit unavailable")
			}
			span.End()
			ch <- result{u.name, v, err}
		}(u)
	}
	values := map[string]T{}
	unavailable := []string{}
	for len(pending) > 0 {
		select {
		case r := <-ch:
			delete(pending, r.name)
			if r.err != nil {
				unavailable = append(unavailable, r.name)
			} else {
				values[r.name] = r.value
			}
		case <-ctx.Done():
			for name := range pending {
				unavailable = append(unavailable, name)
				delete(pending, name)
			}
		}
	}
	sort.Strings(unavailable)
	return values, unavailable
}

func (s *Store) AuditEvents(ctx context.Context, q audit.Query) (audit.Page[contracts.HazardEvent], error) {
	page := audit.Page[contracts.HazardEvent]{Items: []contracts.HazardEvent{}, UnavailableUnits: []string{}}
	c, err := cursorFor(q, "events")
	if err != nil {
		return page, err
	}
	if q.Cursor != "" && (c.At.IsZero() || c.ID == "") {
		return page, audit.ErrQuery
	}
	groups, missing := fanout(ctx, s, func(ctx context.Context, u unit) ([]contracts.HazardEvent, error) {
		var since, after any
		if !q.Since.IsZero() {
			at := q.Since.UTC()
			if remainder := at.Nanosecond() % 1000; remainder != 0 {
				at = at.Add(time.Duration(1000-remainder) * time.Nanosecond)
			}
			since = at
		}
		if !c.At.IsZero() {
			after = c.At
		}
		rows, err := u.readDB.QueryContext(ctx, `SELECT v.data,e.ingested_at FROM hazard_events e JOIN hazard_versions v USING(hazard_id,revision)
  WHERE NOT e.retracted AND ($1='' OR e.source=$1) AND ($2::timestamptz IS NULL OR e.ingested_at>=$2)
  AND ($3::timestamptz IS NULL OR (e.ingested_at,e.hazard_id::text)>($3,$4)) ORDER BY e.ingested_at,e.hazard_id LIMIT $5`, q.Source, since, after, c.ID, q.Limit+1)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		items := []contracts.HazardEvent{}
		for rows.Next() {
			var raw []byte
			var ingested time.Time
			if err := rows.Scan(&raw, &ingested); err != nil {
				return nil, err
			}
			var e contracts.HazardEvent
			decoder := json.NewDecoder(strings.NewReader(string(raw)))
			decoder.UseNumber()
			if err := decoder.Decode(&e); err != nil {
				return nil, err
			}
			e.IngestedAt = ingested.UTC().Format(time.RFC3339Nano)
			items = append(items, e)
		}
		return items, rows.Err()
	})
	for _, items := range groups {
		page.Items = append(page.Items, items...)
	}
	sort.Slice(page.Items, func(i, j int) bool {
		a, _ := time.Parse(time.RFC3339Nano, page.Items[i].IngestedAt)
		b, _ := time.Parse(time.RFC3339Nano, page.Items[j].IngestedAt)
		if a.Equal(b) {
			return page.Items[i].HazardID < page.Items[j].HazardID
		}
		return a.Before(b)
	})
	page.Partial = len(missing) > 0
	page.UnavailableUnits = missing
	if len(page.Items) > q.Limit {
		page.Items = page.Items[:q.Limit]
		if !page.Partial {
			last := page.Items[len(page.Items)-1]
			c.At, _ = time.Parse(time.RFC3339Nano, last.IngestedAt)
			c.ID = last.HazardID
			page.NextCursor = encodeCursor(c)
		}
	}
	return page, nil
}

func (s *Store) AuditChecksum(ctx context.Context, source contracts.Source) (audit.Checksum, error) {
	if source != "" && !source.Valid() {
		return audit.Checksum{}, audit.ErrQuery
	}
	groups, missing := fanout(ctx, s, func(ctx context.Context, u unit) ([]string, error) {
		rows, err := u.readDB.QueryContext(ctx, `SELECT source_ref_id FROM hazard_events WHERE NOT retracted AND ($1='' OR source=$1)`, source)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		refs := []string{}
		for rows.Next() {
			var ref string
			if err := rows.Scan(&ref); err != nil {
				return nil, err
			}
			refs = append(refs, ref)
		}
		return refs, rows.Err()
	})
	refs := []string{}
	for _, r := range groups {
		refs = append(refs, r...)
	}
	sort.Strings(refs)
	hash := sha256.New()
	for i, r := range refs {
		if i > 0 {
			_, _ = hash.Write([]byte{'\n'})
		}
		_, _ = hash.Write([]byte(r))
	}
	return audit.Checksum{Source: source, Count: len(refs), Checksum: hex.EncodeToString(hash.Sum(nil)), Algorithm: "sha256", AsOf: time.Now().UTC().Format(time.RFC3339Nano), Partial: len(missing) > 0, UnavailableUnits: missing}, nil
}

func messageID(source contracts.Source, key string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(string(source) + "\x00" + key))
}
func parseMessageID(id string) (contracts.Source, string, error) {
	if len(id) > 4096 {
		return "", "", audit.ErrQuery
	}
	raw, err := base64.RawURLEncoding.DecodeString(id)
	if err != nil {
		return "", "", audit.ErrQuery
	}
	source, key, ok := strings.Cut(string(raw), "\x00")
	if !ok || !contracts.Source(source).Valid() || key == "" {
		return "", "", audit.ErrQuery
	}
	return contracts.Source(source), key, nil
}

func (s *Store) AuditUnprocessed(ctx context.Context, q audit.Query) (audit.Page[audit.Failed], error) {
	page := audit.Page[audit.Failed]{Items: []audit.Failed{}, UnavailableUnits: []string{}}
	c, err := cursorFor(q, "unprocessed")
	if err != nil {
		return page, err
	}
	groups, missing := fanout(ctx, s, func(ctx context.Context, u unit) ([]audit.Failed, error) {
		if q.Source != "" {
			home, _ := partition.JournalUnit(q.Source)
			if u.name != home {
				return []audit.Failed{}, nil
			}
		}
		rows, err := u.readDB.QueryContext(ctx, `SELECT record_key,source,received_at,reason,attempts,status,sequence FROM ingest_records
  WHERE status<>'done' AND ($1='' OR source=$1) AND (source,sequence)>($2,$3) ORDER BY source,sequence LIMIT $4`, q.Source, c.Source, c.Sequence, q.Limit+1)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		items := []audit.Failed{}
		for rows.Next() {
			var f audit.Failed
			var key string
			var at time.Time
			if err := rows.Scan(&key, &f.Source, &at, &f.Reason, &f.Attempts, &f.Status, &f.Sequence); err != nil {
				return nil, err
			}
			f.MessageID = messageID(f.Source, key)
			f.ReceivedAt = at.UTC().Format(time.RFC3339Nano)
			f.Retryable = f.Status == "retrying"
			items = append(items, f)
		}
		return items, rows.Err()
	})
	for _, items := range groups {
		page.Items = append(page.Items, items...)
	}
	sort.Slice(page.Items, func(i, j int) bool {
		a, b := page.Items[i], page.Items[j]
		if a.Source == b.Source {
			return a.Sequence < b.Sequence
		}
		return a.Source < b.Source
	})
	page.Partial = len(missing) > 0
	page.UnavailableUnits = missing
	if len(page.Items) > q.Limit {
		page.Items = page.Items[:q.Limit]
		if !page.Partial {
			last := page.Items[len(page.Items)-1]
			c.Source = last.Source
			c.Sequence = last.Sequence
			page.NextCursor = encodeCursor(c)
		}
	}
	return page, nil
}

// Keep compile-time checking close to the implementation as interfaces evolve.
var _ audit.Repository = (*Store)(nil)
