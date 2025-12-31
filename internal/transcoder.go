package internal

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

type ProgressCallback func(progress float64)

type TranscodeParams struct {
	SourcePath       string
	DestinationPath  string
	ProgressCallback ProgressCallback
}

type Transcoder interface {
	Transcode(context.Context, TranscodeParams) error
}

func NewTranscoder(profile Profile) Transcoder {
	switch profile {
	case ProfilePreview:
		return &ffmpegTranscoder{}
	default:
		panic(ErrPanicInvalidProfile)
	}
}

type ffmpegTranscoder struct{}

func getResolution(ctx context.Context, path string) (width int, height int, err error) {
	cmd := exec.CommandContext(ctx, "ffprobe",
		"-v", "error",
		"-select_streams", "v:0",
		"-show_entries", "stream=width,height",
		"-of", "csv=p=0",
		path,
	)

	output, err := cmd.Output()
	if err != nil {
		return 0, 0, fmt.Errorf("failed to probe video: %w", err)
	}

	parts := strings.Split(strings.TrimSpace(string(output)), ",")
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("unexpected ffprobe output: %s", output)
	}

	width, err = strconv.Atoi(parts[0])
	if err != nil {
		return 0, 0, fmt.Errorf("failed to parse width: %w", err)
	}

	height, err = strconv.Atoi(parts[1])
	if err != nil {
		return 0, 0, fmt.Errorf("failed to parse height: %w", err)
	}

	return width, height, nil
}

// For now, this only generates preview formats.  Extend it to do more stuff later if necessary.
func (t *ffmpegTranscoder) Transcode(ctx context.Context, params TranscodeParams) error {
	width, height, err := getResolution(ctx, params.SourcePath)
	if err != nil {
		return err
	}

	targetHeight := 240
	targetWidth := int((width * targetHeight) / height)
	if targetWidth%2 != 0 {
		targetWidth++
	}
	resolution := fmt.Sprintf("%dx%d", targetWidth, targetHeight)

	cmd := exec.CommandContext(ctx, "ffmpeg",
		"-skip_frame", "nokey",
		"-i", params.SourcePath,
		"-vf", "fps=1,scale="+resolution,
		"-c:v", "libx264",
		"-ac", "1",
		"-c:a", "aac",
		"-b:a", "32k",
		"-y",
		params.DestinationPath,
	)
	return cmd.Run()
}
