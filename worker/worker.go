package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/krelinga/video-transcoder/internal"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
)

// TranscodeWorker handles video transcoding jobs.
type TranscodeWorker struct {
	river.WorkerDefaults[internal.TranscodeJobArgs]
	DBPool *pgxpool.Pool
}

// Work executes the transcoding job using the appropriate transcoder.
func (w *TranscodeWorker) Work(ctx context.Context, job *river.Job[internal.TranscodeJobArgs]) error {
	args := job.Args

	transcoder := internal.NewTranscoder(args.Profile)

	// Track progress updates for throttling
	lastUpdateTime := time.Now()
	lastProgress := 0.0
	updateInterval := 30 * time.Second
	firstHeartbeatSent := false

	progressCallback := func(currentProgress float64) {
		// Determine if we should send an update:
		// - For heartbeat webhooks: always send the first one immediately, then every 30 seconds
		// - For regular progress: every 30 seconds or on progress change
		shouldUpdate := time.Since(lastUpdateTime) >= updateInterval
		needsFirstHeartbeat := args.HeartbeatWebhookURI != nil && !firstHeartbeatSent

		if shouldUpdate || needsFirstHeartbeat {
			status := internal.TranscodeJobStatus{
				Progress: currentProgress,
			}

			// If heartbeat webhook is configured, enqueue it atomically with job output update
			if args.HeartbeatWebhookURI != nil {
				if err := w.enqueueHeartbeatWebhook(ctx, job, &status); err != nil {
					// Log but don't fail the job on heartbeat webhook errors
					log.Printf("failed to enqueue heartbeat webhook: %v", err)
				} else {
					firstHeartbeatSent = true
				}
			} else {
				// No heartbeat webhook, just record output
				if err := river.RecordOutput(ctx, status); err != nil {
					// Log but don't fail the job on progress update errors
					log.Printf("failed to record output: %v", err)
					return
				}
			}
			lastUpdateTime = time.Now()
			lastProgress = currentProgress
		}
	}

	params := internal.TranscodeParams{
		SourcePath:       args.SourcePath,
		DestinationPath:  args.DestinationPath,
		ProgressCallback: progressCallback,
	}

	if err := transcoder.Transcode(ctx, params); err != nil {
		errMsg := err.Error()
		status := internal.TranscodeJobStatus{
			Progress: lastProgress,
			Error:    &errMsg,
		}
		// Record final error status
		_ = river.RecordOutput(ctx, status)

		// Enqueue webhook job if webhook URI is configured
		if args.WebhookURI != nil {
			if err := w.enqueueWebhook(ctx, job, &status); err != nil {
				return fmt.Errorf("failed to enqueue webhook: %w", err)
			}
			return nil // Job completed via transaction
		}

		return fmt.Errorf("transcoding failed: %w", err)
	}

	// Record final success status
	status := internal.TranscodeJobStatus{
		Progress: 100.0,
	}
	if err := river.RecordOutput(ctx, status); err != nil {
		// Log but don't fail the job on final progress update error
		log.Printf("failed to record final output: %v", err)
	}

	// Enqueue webhook job if webhook URI is configured
	if args.WebhookURI != nil {
		if err := w.enqueueWebhook(ctx, job, &status); err != nil {
			return fmt.Errorf("failed to enqueue webhook: %w", err)
		}
		return nil // Job completed via transaction
	}

	return nil
}

// enqueueWebhook inserts a webhook job in the same transaction that completes this job.
func (w *TranscodeWorker) enqueueWebhook(ctx context.Context, job *river.Job[internal.TranscodeJobArgs], status *internal.TranscodeJobStatus) error {
	webhookArgs := internal.WebhookJobArgs{
		URI:    *job.Args.WebhookURI,
		Token:  job.Args.WebhookToken,
		UUID:   job.Args.UUID,
		Status: status,
	}

	// Start a transaction to insert webhook job and complete transcode job atomically
	tx, err := w.DBPool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	// Get River client from context
	client := river.ClientFromContext[pgx.Tx](ctx)
	if client == nil {
		return fmt.Errorf("no river client in context for webhook job insertion")
	}

	// Insert webhook job within transaction
	if _, err := client.InsertTx(ctx, tx, webhookArgs, nil); err != nil {
		return fmt.Errorf("failed to enqueue webhook job: %w", err)
	}

	// Complete the current job within the same transaction
	if _, err := river.JobCompleteTx[*riverpgxv5.Driver](ctx, tx, job); err != nil {
		return fmt.Errorf("failed to complete job in transaction: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("failed to commit transaction: %w", err)
	}

	return nil
}

// enqueueHeartbeatWebhook inserts a heartbeat webhook job atomically with updating the job output.
// Unlike completion webhooks, heartbeat webhooks use MaxAttempts=1 (no retries) since
// another progress update will follow shortly.
func (w *TranscodeWorker) enqueueHeartbeatWebhook(ctx context.Context, job *river.Job[internal.TranscodeJobArgs], status *internal.TranscodeJobStatus) error {
	webhookArgs := internal.WebhookJobArgs{
		URI:         *job.Args.HeartbeatWebhookURI,
		Token:       job.Args.WebhookToken,
		UUID:        job.Args.UUID,
		Status:      status,
		IsHeartbeat: true,
	}

	// Start a transaction to insert webhook job and update job output atomically
	tx, err := w.DBPool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	// Get River client from context
	client := river.ClientFromContext[pgx.Tx](ctx)
	if client == nil {
		return fmt.Errorf("no river client in context for heartbeat webhook job insertion")
	}

	// Insert webhook job within transaction with no retries
	insertOpts := &river.InsertOpts{MaxAttempts: 1}
	if _, err := client.InsertTx(ctx, tx, webhookArgs, insertOpts); err != nil {
		return fmt.Errorf("failed to enqueue heartbeat webhook job: %w", err)
	}

	// Update the current job's output within the same transaction
	outputParams := &river.JobUpdateParams{Output: status}
	if _, err := client.JobUpdateTx(ctx, tx, job.ID, outputParams); err != nil {
		return fmt.Errorf("failed to update job output in transaction: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("failed to commit transaction: %w", err)
	}

	return nil
}
