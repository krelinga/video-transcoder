package internal

import "github.com/google/uuid"

// TranscodeJobArgs contains the arguments for a transcode job.
// This is used as the River job args payload.
type TranscodeJobArgs struct {
	UUID                uuid.UUID `json:"uuid"`
	SourcePath          string    `json:"sourcePath"`
	DestinationPath     string    `json:"destinationPath"`
	Profile             Profile    `json:"profile"`
	WebhookURI          *string   `json:"webhookUri,omitempty"`
	WebhookToken        []byte    `json:"webhookToken,omitempty"`
	HeartbeatWebhookURI *string   `json:"heartbeatWebhookUri,omitempty"`
}

// Kind returns the job kind identifier for River.
func (TranscodeJobArgs) Kind() string {
	return "transcode"
}

// TranscodeJobStatus represents the current status of a transcode job.
// This is stored as River job output via river.RecordOutput() and can be
// read by both server and worker.
type TranscodeJobStatus struct {
	// Progress is the transcoding progress percentage (0-100).
	Progress float64 `json:"progress"`
	// Error contains an error message if the job failed.
	Error *string `json:"error,omitempty"`
}

// WebhookJobArgs contains the arguments for a webhook notification job.
type WebhookJobArgs struct {
	URI         string              `json:"uri"`
	Token       []byte              `json:"token,omitempty"`
	UUID        uuid.UUID           `json:"uuid"`
	Status      *TranscodeJobStatus `json:"status,omitempty"`
	IsHeartbeat bool                `json:"isHeartbeat,omitempty"`
}

// Kind returns the job kind identifier for River.
func (WebhookJobArgs) Kind() string {
	return "webhook"
}
