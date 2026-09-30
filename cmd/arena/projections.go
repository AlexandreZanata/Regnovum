package main

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/platform/clockseed"
	"github.com/AlexandreZanata/Regnovum/internal/platform/config"
	"github.com/AlexandreZanata/Regnovum/internal/platform/dbpool"
	"github.com/AlexandreZanata/Regnovum/internal/platform/logging"
	statpostgres "github.com/AlexandreZanata/Regnovum/internal/statprojections/adapters/postgres"
	statapp "github.com/AlexandreZanata/Regnovum/internal/statprojections/application"
)

const projectionsUsage = `rebuild derived public statistics projections.

Usage:

  arena projections rebuild [--batch-size N] [--cursor UUID] [--transparency-start RFC3339 --transparency-end RFC3339]

Arena rows are rebuilt in bounded UUID-keyset batches. If the process stops,
repeat the command with the last reported cursor; each batch is independently
committed. The normalized source tables remain authoritative.`

func runProjections(args []string, stdout *os.File) error {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		fmt.Fprintln(stdout, projectionsUsage)
		return nil
	}
	if args[0] != "rebuild" {
		return fmt.Errorf("unknown projections subcommand %q\n\n%s", args[0], projectionsUsage)
	}

	batchSize := statapp.DefaultBatchSize
	cursor := ""
	var start, end time.Time
	for i := 1; i < len(args); i++ {
		switch {
		case args[i] == "--batch-size" && i+1 < len(args):
			i++
			parsed, err := strconv.Atoi(args[i])
			if err != nil {
				return fmt.Errorf("invalid --batch-size %q", args[i])
			}
			batchSize = parsed
		case args[i] == "--cursor" && i+1 < len(args):
			i++
			cursor = args[i]
		case strings.HasPrefix(args[i], "--transparency-start="):
			parsed, err := time.Parse(time.RFC3339, strings.TrimPrefix(args[i], "--transparency-start="))
			if err != nil {
				return fmt.Errorf("invalid transparency start: %w", err)
			}
			start = parsed.UTC()
		case strings.HasPrefix(args[i], "--transparency-end="):
			parsed, err := time.Parse(time.RFC3339, strings.TrimPrefix(args[i], "--transparency-end="))
			if err != nil {
				return fmt.Errorf("invalid transparency end: %w", err)
			}
			end = parsed.UTC()
		default:
			return fmt.Errorf("unknown projections option %q\n\n%s", args[i], projectionsUsage)
		}
	}
	if start.IsZero() != end.IsZero() {
		return fmt.Errorf("transparency start and end must be supplied together")
	}

	cfg := config.MustLoad()
	dsn := string(cfg.DatabaseURL().Unredacted())
	if dsn == "" {
		return fmt.Errorf("ARENA_DATABASE_URL is required for arena projections")
	}
	logger := logging.New(stdout, cfg.LogLevel())
	pool, err := dbpool.New(context.Background(), dsn, dbpool.FromConfig(cfg), logger, clockseed.NewClock())
	if err != nil {
		return fmt.Errorf("initialize database pool: %w", err)
	}
	defer pool.Close()

	repository := statpostgres.NewRepository(pool.Pool())
	useCase, err := statapp.NewRebuildUseCase(repository, batchSize)
	if err != nil {
		return err
	}
	for {
		batch, err := useCase.RebuildArenaBatch(context.Background(), cursor)
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "arena projection batch: processed=%d cursor=%s\n", batch.Processed, batch.NextCursor)
		if batch.NextCursor == "" {
			break
		}
		cursor = batch.NextCursor
	}
	if !start.IsZero() {
		if err := useCase.RebuildTransparency(context.Background(), start, end); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "transparency projection rebuilt: %s/%s\n", start.Format(time.RFC3339), end.Format(time.RFC3339))
	}
	return nil
}
