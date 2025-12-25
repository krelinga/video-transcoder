package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/krelinga/video-transcoder/internal"
	"github.com/riverqueue/river"
)

// WebhookPayload is the JSON body sent to the webhook URI.
type WebhookPayload struct {
	Token    []byte    `json:"token,omitempty"`
	UUID     uuid.UUID `json:"uuid"`
	Error    *string   `json:"error,omitempty"`
	Progress *float64  `json:"progress,omitempty"`
}

// WebhookWorker handles webhook notification jobs.
type WebhookWorker struct {
	river.WorkerDefaults[internal.WebhookJobArgs]
	HTTPClient *http.Client
}

// Work sends a POST request to the configured webhook URI.
func (w *WebhookWorker) Work(ctx context.Context, job *river.Job[internal.WebhookJobArgs]) error {
	payload := WebhookPayload{
		Token: job.Args.Token,
		UUID:  job.Args.UUID,
	}
	if job.Args.Status != nil {
		payload.Error = job.Args.Status.Error
		if job.Args.IsHeartbeat {
			payload.Progress = &job.Args.Status.Progress
		}
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal webhook payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, job.Args.URI, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("failed to create webhook request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	client := w.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send webhook request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("webhook request failed with status %d", resp.StatusCode)
	}

	return nil
}
