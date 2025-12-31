package internal

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
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

func getDuration(ctx context.Context, path string) (time.Duration, error) {
	cmd := exec.CommandContext(ctx, "ffprobe",
		"-v", "error",
		"-show_entries", "format=duration",
		"-of", "csv=p=0",
		path,
	)

	output, err := cmd.Output()
	if err != nil {
		return 0, fmt.Errorf("failed to probe duration: %w", err)
	}

	durationSec, err := strconv.ParseFloat(strings.TrimSpace(string(output)), 64)
	if err != nil {
		return 0, fmt.Errorf("failed to parse duration: %w", err)
	}

	return time.Duration(durationSec * float64(time.Second)), nil
}

var timeRegex = regexp.MustCompile(`time=(\d{2}):(\d{2}):(\d{2})\.(\d{2})`)

func parseFfmpegProgress(line string, totalDuration time.Duration) (float64, bool) {
	matches := timeRegex.FindStringSubmatch(line)
	if len(matches) != 5 {
		return 0, false
	}

	hours, _ := strconv.Atoi(matches[1])
	minutes, _ := strconv.Atoi(matches[2])
	seconds, _ := strconv.Atoi(matches[3])
	centiseconds, _ := strconv.Atoi(matches[4])

	currentTime := time.Duration(hours)*time.Hour +
		time.Duration(minutes)*time.Minute +
		time.Duration(seconds)*time.Second +
		time.Duration(centiseconds)*10*time.Millisecond

	if totalDuration == 0 {
		return 0, false
	}

	progress := float64(currentTime) / float64(totalDuration)
	if progress > 1.0 {
		progress = 1.0
	}
	return progress, true
}

// For now, this only generates preview formats.  Extend it to do more stuff later if necessary.
func (t *ffmpegTranscoder) Transcode(ctx context.Context, params TranscodeParams) error {
	width, height, err := getResolution(ctx, params.SourcePath)
	if err != nil {
		return err
	}

	var totalDuration time.Duration
	if params.ProgressCallback != nil {
		totalDuration, err = getDuration(ctx, params.SourcePath)
		if err != nil {
			return err
		}
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
		"-progress", "pipe:2",
		"-y",
		params.DestinationPath,
	)

	if params.ProgressCallback != nil {
		stderrPipe, err := cmd.StderrPipe()
		if err != nil {
			return fmt.Errorf("failed to create stderr pipe: %w", err)
		}

		if err := cmd.Start(); err != nil {
			return fmt.Errorf("failed to start ffmpeg: %w", err)
		}

		scanner := bufio.NewScanner(stderrPipe)
		go func() {
			for scanner.Scan() {
				line := scanner.Text()
				if progress, ok := parseFfmpegProgress(line, totalDuration); ok {
					params.ProgressCallback(progress * 100) // Convert to percentage
				}
			}
		}()

		if err := cmd.Wait(); err != nil {
			return fmt.Errorf("ffmpeg failed: %w", err)
		}

		// Consume any remaining output
		io.Copy(io.Discard, stderrPipe)

		return nil
	}

	return cmd.Run()
}
