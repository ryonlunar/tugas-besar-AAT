package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/ryonlunar/tugas-besar-AAT/services/aggregator/internal/mapper"
	"github.com/ryonlunar/tugas-besar-AAT/services/aggregator/internal/store"
)

var historySourceIDPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

type historyAuditStore interface {
	FindHazardID(context.Context, mapper.Source, string) (string, bool, error)
	GetVersion(context.Context, string, int64) (json.RawMessage, bool, error)
}

type historySnapshot struct {
	Revision int64           `json:"revision"`
	Data     json.RawMessage `json:"data"`
}

type historyAuditResult struct {
	HazardID    string            `json:"hazard_id"`
	Source      mapper.Source     `json:"source"`
	SourceRefID string            `json:"source_ref_id"`
	Versions    []historySnapshot `json:"versions"`
}

func runHistoryAudit(ctx context.Context, repository historyAuditStore, args []string, output io.Writer) error {
	flags := flag.NewFlagSet("history-audit", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	sourceValue := flags.String("source", "", "source name: BMKG or PVMBG")
	sourceRefID := flags.String("source-ref-id", "", "synthetic source record identifier")
	revisionsValue := flags.String("revisions", "", "comma-separated revision numbers to read")
	if err := flags.Parse(args); err != nil {
		return errors.New("invalid history-audit arguments")
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected history-audit arguments")
	}
	source := mapper.Source(*sourceValue)
	if source != mapper.SourceBMKG && source != mapper.SourcePVMBG {
		return errors.New("source must be BMKG or PVMBG")
	}
	if !historySourceIDPattern.MatchString(*sourceRefID) {
		return errors.New("source-ref-id must contain 1-64 ASCII letters, digits, dot, underscore, or hyphen")
	}
	revisions, err := parseHistoryRevisions(*revisionsValue)
	if err != nil {
		return err
	}

	hazardID, found, err := repository.FindHazardID(ctx, source, *sourceRefID)
	if err != nil {
		return fmt.Errorf("find selected source record: %w", err)
	}
	if !found {
		return errors.New("selected source record was not found")
	}
	result := historyAuditResult{HazardID: hazardID, Source: source, SourceRefID: *sourceRefID, Versions: make([]historySnapshot, 0, len(revisions))}
	for _, revision := range revisions {
		data, found, err := repository.GetVersion(ctx, hazardID, revision)
		if err != nil {
			return fmt.Errorf("read selected revision %d: %w", revision, err)
		}
		if !found {
			return fmt.Errorf("selected revision %d was not found", revision)
		}
		if !json.Valid(data) {
			return fmt.Errorf("selected revision %d contains invalid stored JSON", revision)
		}
		result.Versions = append(result.Versions, historySnapshot{Revision: revision, Data: data})
	}
	encoder := json.NewEncoder(output)
	encoder.SetIndent("", "  ")
	return encoder.Encode(result)
}

func parseHistoryRevisions(value string) ([]int64, error) {
	parts := strings.Split(value, ",")
	if value == "" || len(parts) > 20 {
		return nil, errors.New("revisions must select between 1 and 20 revisions")
	}
	seen := make(map[int64]struct{}, len(parts))
	revisions := make([]int64, 0, len(parts))
	for _, part := range parts {
		revision, err := strconv.ParseInt(strings.TrimSpace(part), 10, 64)
		if err != nil || revision < 1 {
			return nil, errors.New("revisions must be positive integers")
		}
		if _, duplicate := seen[revision]; duplicate {
			return nil, errors.New("revisions must not contain duplicates")
		}
		seen[revision] = struct{}{}
		revisions = append(revisions, revision)
	}
	return revisions, nil
}

func runHistoryAuditCommand(args []string) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	db, err := openDB(cfg.databaseURL, cfg.readConns)
	if err != nil {
		return errors.New("open Aggregator history store")
	}
	defer db.Close()
	repository := store.New(db, cfg.staleAfter)
	return runHistoryAudit(ctx, repository, args, os.Stdout)
}
