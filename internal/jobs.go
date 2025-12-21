package internal

import "github.com/google/uuid"

// TranscodeJobArgs contains the arguments for a transcode job.
// This is used as the River job args payload.
type TranscodeJobArgs struct {
	UUID            uuid.UUID `json:"uuid"`
	SourcePath      string    `json:"sourcePath"`
	DestinationPath string    `json:"destinationPath"`
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
	Progress int `json:"progress"`
	// Error contains an error message if the job failed.
	Error *string `json:"error,omitempty"`
}
