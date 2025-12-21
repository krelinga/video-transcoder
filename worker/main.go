package main

import (
	"context"
	"fmt"
	"log"
	"os/signal"
	"syscall"
	"time"

	"github.com/krelinga/video-transcoder/internal"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
)

func main() {
	if err := run(); err != nil {
		log.Fatalf("worker error: %v", err)
	}
}

func run() error {
	// Create context that listens for shutdown signals
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Load configuration
	cfg := internal.NewWorkerConfigFromEnv()

	// Create database pool
	pool, err := internal.NewDBPool(ctx, cfg.Database)
	if err != nil {
		return fmt.Errorf("failed to create database pool: %w", err)
	}
	defer pool.Close()

	// Run migrations
	log.Println("Running database migrations...")
	if err := internal.MigrateUp(ctx, pool); err != nil {
		return fmt.Errorf("failed to run migrations: %w", err)
	}
	log.Println("Migrations complete")

	// Create River workers and register transcode worker
	workers := river.NewWorkers()
	river.AddWorker(workers, &TranscodeWorker{})

	// Create River client with workers
	riverClient, err := river.NewClient(riverpgxv5.New(pool), &river.Config{
		Queues: map[string]river.QueueConfig{
			river.QueueDefault: {MaxWorkers: 1},
		},
		Workers: workers,
	})
	if err != nil {
		return fmt.Errorf("failed to create river client: %w", err)
	}

	// Start River client to begin processing jobs
	if err := riverClient.Start(ctx); err != nil {
		return fmt.Errorf("failed to start river client: %w", err)
	}

	log.Println("Worker started, waiting for jobs...")

	// Wait for shutdown signal
	<-ctx.Done()
	log.Println("Shutdown signal received, shutting down gracefully...")

	// Create shutdown context with timeout
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Stop River client gracefully
	if err := riverClient.Stop(shutdownCtx); err != nil {
		return fmt.Errorf("river client shutdown error: %w", err)
	}

	log.Println("Worker shutdown complete")
	return nil
}
