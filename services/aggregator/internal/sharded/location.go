package sharded

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/ryonlunar/tugas-besar-AAT/services/aggregator/internal/audit"
)

func (s *Store) AuditLocation(ctx context.Context, id string) (audit.Locations, bool, error) {
	result := audit.Locations{HazardID: id, Locations: []audit.Location{}, UnavailableUnits: []string{}}
	present, missing := fanout(ctx, s, func(ctx context.Context, u unit) (bool, error) {
		var found bool
		err := u.readDB.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM hazard_events WHERE hazard_id::text=$1 AND NOT retracted)`, id).Scan(&found)
		return found, err
	})
	target := ""
	for name, found := range present {
		if found {
			if target != "" {
				return result, false, errors.New("duplicate canonical identity")
			}
			target = name
		}
	}
	result.UnavailableUnits = missing
	result.Partial = len(missing) > 0
	if target == "" {
		return result, result.Partial, nil
	}
	pods, err := s.topology(ctx)
	if err != nil {
		result.Partial = true
		result.UnavailableUnits = append(result.UnavailableUnits, target)
		return result, true, nil
	}
	type copyResult struct {
		pod   audit.Pod
		found bool
		err   error
	}
	ch := make(chan copyResult, len(pods))
	count := 0
	pending := map[string]bool{}
	queryCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	for _, pod := range pods {
		if pod.Unit != target {
			continue
		}
		count++
		pending[pod.Name] = true
		go func(pod audit.Pod) {
			r := copyResult{pod: pod}
			if !pod.Ready || pod.IP == "" {
				r.err = errors.New("replica unready")
			} else {
				config, err := pgx.ParseConfig(s.lookup(target).dsn)
				if err == nil {
					// Keep the Service's TLS ServerName/CA while dialing this physical copy.
					config.Host = pod.IP
					var conn *pgx.Conn
					conn, err = pgx.ConnectConfig(queryCtx, config)
					if err == nil {
						err = conn.QueryRow(queryCtx, `SELECT EXISTS(SELECT 1 FROM hazard_events WHERE hazard_id::text=$1 AND NOT retracted)`, id).Scan(&r.found)
						closeCtx, stop := context.WithTimeout(context.Background(), time.Second)
						_ = conn.Close(closeCtx)
						stop()
					}
				}
				r.err = err
			}
			ch <- r
		}(pod)
	}
	if count != 3 {
		result.Partial = true
		result.UnavailableUnits = append(result.UnavailableUnits, target)
	}
	for len(pending) > 0 {
		select {
		case r := <-ch:
			delete(pending, r.pod.Name)
			if r.err != nil || !r.found {
				result.Partial = true
				result.UnavailableUnits = append(result.UnavailableUnits, r.pod.Name)
				continue
			}
			name := r.pod.Name
			if r.pod.Role == "primary" {
				name = target
			}
			result.Locations = append(result.Locations, audit.Location{Node: r.pod.Node, Unit: name, Role: r.pod.Role})
		case <-queryCtx.Done():
			result.Partial = true
			for name := range pending {
				result.UnavailableUnits = append(result.UnavailableUnits, name)
				delete(pending, name)
			}
		}
	}
	sort.Slice(result.Locations, func(i, j int) bool { return result.Locations[i].Node < result.Locations[j].Node })
	sort.Strings(result.UnavailableUnits)
	return result, true, nil
}
