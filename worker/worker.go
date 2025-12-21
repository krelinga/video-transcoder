package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"time"

	"github.com/krelinga/video-transcoder/internal"
	"github.com/riverqueue/river"
)

// TranscodeWorker handles video transcoding jobs using HandBrake.
type TranscodeWorker struct {
	river.WorkerDefaults[internal.TranscodeJobArgs]
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

	return nil
}
