package videotranscoder

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/docker/docker/api/types/build"
	"github.com/google/uuid"
	"github.com/krelinga/video-transcoder/vtrest"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/network"
	"github.com/testcontainers/testcontainers-go/wait"
)

func TestTranscodeEndToEnd(t *testing.T) {
	ctx := context.Background()

	// Create temp directory for media files
	tempDir, err := os.MkdirTemp("", "transcode-e2e-*")
	if err != nil {
		t.Fatalf("failed to create temp directory: %v", err)
	}

	// Copy test file to temp directory
	srcFile := "testdata/testdata_sample_640x360.mkv"
	dstFile := filepath.Join(tempDir, "testdata_sample_640x360.mkv")
	if err := copyFile(srcFile, dstFile); err != nil {
		t.Fatalf("failed to copy test file: %v", err)
	}

	// Create Docker network
	net, err := network.New(ctx, network.WithCheckDuplicate())
	if err != nil {
		t.Fatalf("failed to create network: %v", err)
	}
	networkName := net.Name

	// Database configuration
	dbName := "videotranscoder"
	dbUser := "postgres"
	dbPassword := "postgres"

	// Start postgres container
	postgresReq := testcontainers.ContainerRequest{
		Image:        "postgres:16",
		ExposedPorts: []string{"5432/tcp"},
		Env: map[string]string{
			"POSTGRES_DB":       dbName,
			"POSTGRES_USER":     dbUser,
			"POSTGRES_PASSWORD": dbPassword,
		},
		Networks:       []string{networkName},
		NetworkAliases: map[string][]string{networkName: {"postgres"}},
		WaitingFor:     wait.ForLog("database system is ready to accept connections").WithOccurrence(2),
	}
	postgresContainer, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: postgresReq,
		Started:          true,
	})
	if err != nil {
		t.Fatalf("failed to start postgres container: %v", err)
	}
	t.Cleanup(func() {
		dumpContainerLogs(t, ctx, postgresContainer, "postgres")
	})

	// Common environment variables for server and worker
	dbEnv := map[string]string{
		"VT_DB_HOST":     "postgres",
		"VT_DB_PORT":     "5432",
		"VT_DB_USER":     dbUser,
		"VT_DB_PASSWORD": dbPassword,
		"VT_DB_NAME":     dbName,
		"VT_SERVER_PORT": "8080",
	}

	// Build and start server container
	serverReq := testcontainers.ContainerRequest{
		FromDockerfile: testcontainers.FromDockerfile{
			Context:    ".",
			Dockerfile: "Dockerfile",
			BuildArgs:  map[string]*string{},
			BuildOptionsModifier: func(buildOptions *build.ImageBuildOptions) {
				buildOptions.Target = "server"
			},
		},
		ExposedPorts:   []string{"8080/tcp"},
		Env:            dbEnv,
		Networks:       []string{networkName},
		NetworkAliases: map[string][]string{networkName: {"server"}},
		WaitingFor:     wait.ForLog("Starting HTTP server on port 8080"),
	}
	serverContainer, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: serverReq,
		Started:          true,
	})
	if err != nil {
		t.Fatalf("failed to start server container: %v", err)
	}
	t.Cleanup(func() {
		dumpContainerLogs(t, ctx, serverContainer, "server")
	})

	// Build and start worker container with volume mount
	workerReq := testcontainers.ContainerRequest{
		FromDockerfile: testcontainers.FromDockerfile{
			Context:    ".",
			Dockerfile: "Dockerfile",
			BuildArgs:  map[string]*string{},
			BuildOptionsModifier: func(buildOptions *build.ImageBuildOptions) {
				buildOptions.Target = "worker"
			},
		},
		Env:            dbEnv,
		Networks:       []string{networkName},
		NetworkAliases: map[string][]string{networkName: {"worker"}},
		Mounts: testcontainers.Mounts(
			testcontainers.BindMount(tempDir, "/nas/media"),
		),
		WaitingFor: wait.ForLog("Worker started, waiting for jobs..."),
	}
	workerContainer, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: workerReq,
		Started:          true,
	})
	if err != nil {
		t.Fatalf("failed to start worker container: %v", err)
	}
	t.Cleanup(func() {
		dumpContainerLogs(t, ctx, workerContainer, "worker")
	})

	// Get server mapped port
	mappedPort, err := serverContainer.MappedPort(ctx, "8080")
	if err != nil {
		t.Fatalf("failed to get server mapped port: %v", err)
	}
	serverHost, err := serverContainer.Host(ctx)
	if err != nil {
		t.Fatalf("failed to get server host: %v", err)
	}
	serverURL := fmt.Sprintf("http://%s:%s", serverHost, mappedPort.Port())

	// Create vtrest client
	client, err := vtrest.NewClientWithResponses(serverURL)
	if err != nil {
		t.Fatalf("failed to create vtrest client: %v", err)
	}

	// Create transcode job
	jobUUID := uuid.New()
	sourcePath := "/nas/media/testdata_sample_640x360.mkv"
	destPath := "/nas/media/output.mp4"

	createResp, err := client.CreateTranscodeWithResponse(ctx, vtrest.CreateTranscodeJSONRequestBody{
		Uuid:            jobUUID,
		SourcePath:      sourcePath,
		DestinationPath: destPath,
	})
	if err != nil {
		t.Fatalf("failed to create transcode job: %v", err)
	}
	if createResp.JSON201 == nil {
		t.Fatalf("expected 201 response, got status %d: %s", createResp.StatusCode(), string(createResp.Body))
	}

	t.Logf("Created transcode job with UUID: %s", jobUUID)

	// Poll for job completion
	var finalStatus vtrest.TranscodeStatus
	for {
		statusResp, err := client.GetTranscodeStatusWithResponse(ctx, jobUUID)
		if err != nil {
			t.Fatalf("failed to get transcode status: %v %v", err, statusResp)
		}
		if statusResp.JSON200 == nil {
			t.Fatalf("expected 200 response, got status %d: %s", statusResp.StatusCode(), string(statusResp.Body))
		}

		job := statusResp.JSON200
		t.Logf("Job status: %s, progress: %d%%", job.Status, job.Progress)

		if job.Status == vtrest.Completed || job.Status == vtrest.Failed {
			finalStatus = job.Status
			if job.Error != nil {
				t.Logf("Job error: %s", *job.Error)
			}
			break
		}

		time.Sleep(2 * time.Second)
	}

	// Verify job completed successfully
	if finalStatus != vtrest.Completed {
		t.Fatalf("expected job to complete successfully, but got status: %s", finalStatus)
	}

	// Verify output file exists
	outputFile := filepath.Join(tempDir, "output.mp4")
	if _, err := os.Stat(outputFile); os.IsNotExist(err) {
		t.Fatalf("output file does not exist: %s", outputFile)
	}

	t.Logf("Transcode completed successfully, output file exists at: %s", outputFile)

	// Test duplicate UUID rejection - try to create another job with same UUID but different destination
	duplicateDestPath := "/nas/media/output_duplicate.mp4"
	duplicateResp, err := client.CreateTranscodeWithResponse(ctx, vtrest.CreateTranscodeJSONRequestBody{
		Uuid:            jobUUID,
		SourcePath:      sourcePath,
		DestinationPath: duplicateDestPath,
	})
	if err != nil {
		t.Fatalf("failed to send duplicate transcode request: %v", err)
	}
	if duplicateResp.StatusCode() != 409 {
		t.Fatalf("expected 409 response for duplicate UUID, got status %d: %s", duplicateResp.StatusCode(), string(duplicateResp.Body))
	}
	if duplicateResp.JSON409 == nil {
		t.Fatalf("expected JSON409 response body for duplicate UUID")
	}
	t.Logf("Duplicate UUID correctly rejected with 409: %s", duplicateResp.JSON409.Message)

}

// copyFile copies a file from src to dst
func copyFile(src, dst string) error {
	srcFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer srcFile.Close()

	dstFile, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer dstFile.Close()

	_, err = io.Copy(dstFile, srcFile)
	return err
}

// dumpContainerLogs reads and logs all output from a container
func dumpContainerLogs(t *testing.T, ctx context.Context, container testcontainers.Container, name string) {
	logs, err := container.Logs(ctx)
	if err != nil {
		t.Logf("failed to get %s container logs: %v", name, err)
		return
	}
	defer logs.Close()

	logBytes, err := io.ReadAll(logs)
	if err != nil {
		t.Logf("failed to read %s container logs: %v", name, err)
		return
	}

	t.Logf("=== %s container logs ===\n%s", name, string(logBytes))
}
