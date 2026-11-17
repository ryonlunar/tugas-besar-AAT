package sharded

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/ryonlunar/tugas-besar-AAT/services/aggregator/internal/store"
)

func (s *Store) PendingOutbox(ctx context.Context, limit int, now time.Time) ([]store.OutboxMessage, error) {
	type result struct {
		index    int
		messages []store.OutboxMessage
		err      error
	}
	results := make(chan result, len(s.units))
	for i, u := range s.units {
		go func(i int, u unit) {
			messages, err := u.store.PendingOutbox(ctx, limit, now)
			for j := range messages {
				messages[j].QueueKey = u.name + "/" + messages[j].MessageID
			}
			results <- result{i, messages, err}
		}(i, u)
	}
	batches := make([][]store.OutboxMessage, len(s.units))
	available := 0
	for range s.units {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case r := <-results:
			if r.err == nil {
				batches[r.index] = r.messages
				available++
			}
		}
	}
	if available == 0 {
		return nil, errors.New("all outbox units unavailable")
	}
	messages := []store.OutboxMessage{}
	for index := 0; len(messages) < limit; index++ {
		added := false
		for _, batch := range batches {
			if index < len(batch) {
				messages = append(messages, batch[index])
				added = true
				if len(messages) == limit {
					break
				}
			}
		}
		if !added {
			break
		}
	}
	return messages, nil
}

func (s *Store) queue(key string) (*store.Store, string, error) {
	name, id, ok := strings.Cut(key, "/")
	if !ok || id == "" {
		return nil, "", errors.New("invalid outbox routing key")
	}
	for _, u := range s.units {
		if u.name == name {
			return u.store, id, nil
		}
	}
	return nil, "", errors.New("unknown outbox unit")
}
func (s *Store) MarkPublished(ctx context.Context, key string, at time.Time) error {
	repository, id, err := s.queue(key)
	if err != nil {
		return err
	}
	return repository.MarkPublished(ctx, id, at)
}
func (s *Store) RecordPublishFailure(ctx context.Context, key, code string, next time.Time) error {
	repository, id, err := s.queue(key)
	if err != nil {
		return err
	}
	return repository.RecordPublishFailure(ctx, id, code, next)
}
