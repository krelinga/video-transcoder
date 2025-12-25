package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/krelinga/video-transcoder/internal"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
)

// TranscodeWorker handles video transcoding jobs using HandBrake.
type TranscodeWorker struct {
	river.WorkerDefaults[internal.TranscodeJobArgs]
	DBPool *pgxpool.Pool
}

// handbrakeProgress represents the JSON progress output from HandBrake.
type handbrakeProgress struct {
	State    string  `json:"State"`
	Progress float64 `json:"Progress"`
}

// Work executes the transcoding job using HandBrake CLI.
func (w *TranscodeWorker) Work(ctx context.Context, job *river.Job[internal.TranscodeJobArgs]) error {
	args := job.Args

	// Build HandBrake command
	cmd := exec.CommandContext(ctx,
		"HandBrakeCLI",
		"-i", args.SourcePath,
		"-o", args.DestinationPath,
		"--json",
		"--preset", "Fast 1080p30",
	)

	// Get stderr pipe for progress output
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("failed to get stderr pipe: %w", err)
	}

	// Start the command
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start HandBrake: %w", err)
	}

	// Track progress updates
	lastUpdateTime := time.Now()
	lastProgress := 0
	updateInterval := 30 * time.Second

	// Parse JSON progress from stderr
	scanner := bufio.NewScanner(stderr)
	for scanner.Scan() {
		line := scanner.Text()

		var progress handbrakeProgress
		if err := json.Unmarshal([]byte(line), &progress); err != nil {
			// Not all lines are JSON, skip non-JSON output
			continue
		}

		if progress.State == "WORKING" {
			currentProgress := int(progress.Progress * 100)

			// Update progress every 30 seconds or if progress changed significantly
			if time.Since(lastUpdateTime) >= updateInterval || currentProgress != lastProgress {
				status := internal.TranscodeJobStatus{
					Progress: currentProgress,
				}
				if err := river.RecordOutput(ctx, status); err != nil {
					// Log but don't fail the job on progress update errors
					continue
				}
				lastUpdateTime = time.Now()
				lastProgress = currentProgress
			}
		}
	}

	// Wait for command to complete
	if err := cmd.Wait(); err != nil {
		errMsg := fmt.Sprintf("HandBrake failed: %v", err)
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

		return fmt.Errorf("HandBrake execution failed: %w", err)
	}

	// Record final success status
	status := internal.TranscodeJobStatus{
		Progress: 100,
	}
	if err := river.RecordOutput(ctx, status); err != nil {
		// Log but don't fail the job on final progress update error
		return nil
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
