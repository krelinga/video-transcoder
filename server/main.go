package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	"github.com/krelinga/video-transcoder/internal"
	"github.com/krelinga/video-transcoder/vtrest"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
)

func main() {
	if err := run(); err != nil {
		log.Fatalf("server error: %v", err)
	}
}

func run() error {
	// Create context that listens for shutdown signals
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Load configuration
	cfg := internal.NewServerConfigFromEnv()

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

	// Create River client (insert-only, no workers)
	riverClient, err := river.NewClient(riverpgxv5.New(pool), &river.Config{
		// No workers needed for the server - it only inserts jobs
		Workers: nil,
	})
	if err != nil {
		return fmt.Errorf("failed to create river client: %w", err)
	}

	// Create server and wire up HTTP handlers
	server := NewServer(pool, riverClient)
	strictHandler := vtrest.NewStrictHandler(server, nil)
	httpHandler := vtrest.Handler(strictHandler)

	// Configure HTTP server
	httpServer := &http.Server{
		Addr:    fmt.Sprintf(":%d", cfg.Port),
		Handler: httpHandler,
	}

	// Start HTTP server in goroutine
	serverErr := make(chan error, 1)
	go func() {
		log.Printf("Starting HTTP server on port %d", cfg.Port)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			serverErr <- err
		}
		close(serverErr)
	}()

	// Wait for shutdown signal or server error
	select {
	case err := <-serverErr:
		return fmt.Errorf("HTTP server error: %w", err)
	case <-ctx.Done():
		log.Println("Shutdown signal received, shutting down gracefully...")
	}

	// Create shutdown context with timeout
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Shutdown HTTP server
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("HTTP server shutdown error: %w", err)
	}

	log.Println("Server shutdown complete")
	return nil
}
