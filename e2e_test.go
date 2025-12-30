package videotranscoder

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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

	// Start MockServer container for webhook testing
	mockServerReq := testcontainers.ContainerRequest{
		Image:          "mockserver/mockserver:5.15.0",
		ExposedPorts:   []string{"1080/tcp"},
		Networks:       []string{networkName},
		NetworkAliases: map[string][]string{networkName: {"mockserver"}},
		WaitingFor:     wait.ForLog("started on port: 1080"),
	}
	mockServerContainer, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: mockServerReq,
		Started:          true,
	})
	if err != nil {
		t.Fatalf("failed to start mockserver container: %v", err)
	}
	t.Cleanup(func() {
		dumpContainerLogs(t, ctx, mockServerContainer, "mockserver")
	})

	// Get MockServer mapped port
	mockServerPort, err := mockServerContainer.MappedPort(ctx, "1080")
	if err != nil {
		t.Fatalf("failed to get mockserver mapped port: %v", err)
	}
	mockServerHost, err := mockServerContainer.Host(ctx)
	if err != nil {
		t.Fatalf("failed to get mockserver host: %v", err)
	}
	mockServerURL := fmt.Sprintf("http://%s:%s", mockServerHost, mockServerPort.Port())

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

	// Sub-test: Basic transcode without webhook
	t.Run("without webhook", func(t *testing.T) {
		// Create transcode job
		jobUUID := uuid.New()
		sourcePath := "/nas/media/testdata_sample_640x360.mkv"
		destPath := "/nas/media/output.mp4"

		createResp, err := client.CreateTranscodeWithResponse(ctx, vtrest.CreateTranscodeJSONRequestBody{
			Uuid:            jobUUID,
			SourcePath:      sourcePath,
			DestinationPath: destPath,
			Profile:         "preview",
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
			t.Logf("Job status: %s, progress: %.2f%%", job.Status, job.Progress)

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
			Profile:         "preview",
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
	})

	// Sub-test: Transcode with webhook
	t.Run("with webhook", func(t *testing.T) {
		// Set up MockServer expectation for webhook
		setupMockServerExpectation(t, mockServerURL, "/webhook")

		// Create transcode job with webhook
		jobUUID := uuid.New()
		sourcePath := "/nas/media/testdata_sample_640x360.mkv"
		destPath := "/nas/media/output_webhook.mp4"
		webhookURI := "http://mockserver:1080/webhook"
		webhookToken := []byte("test-webhook-token")

		createResp, err := client.CreateTranscodeWithResponse(ctx, vtrest.CreateTranscodeJSONRequestBody{
			Uuid:            jobUUID,
			SourcePath:      sourcePath,
			DestinationPath: destPath,
			Profile:         "preview",
			WebhookUri:      &webhookURI,
			WebhookToken:    webhookToken,
		})
		if err != nil {
			t.Fatalf("failed to create transcode job: %v", err)
		}
		if createResp.JSON201 == nil {
			t.Fatalf("expected 201 response, got status %d: %s", createResp.StatusCode(), string(createResp.Body))
		}

		t.Logf("Created transcode job with UUID: %s and webhook URI: %s", jobUUID, webhookURI)

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
			t.Logf("Job status: %s, progress: %.2f%%", job.Status, job.Progress)

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
		outputFile := filepath.Join(tempDir, "output_webhook.mp4")
		if _, err := os.Stat(outputFile); os.IsNotExist(err) {
			t.Fatalf("output file does not exist: %s", outputFile)
		}

		t.Logf("Transcode completed successfully, output file exists at: %s", outputFile)

		// Wait for and verify webhook was called
		webhookPayload := waitForWebhook(t, ctx, mockServerURL, "/webhook", 30*time.Second)
		if webhookPayload == nil {
			t.Fatalf("webhook was not called within timeout")
		}

		// Verify webhook payload
		if webhookPayload.UUID != jobUUID {
			t.Errorf("webhook UUID mismatch: got %s, want %s", webhookPayload.UUID, jobUUID)
		}
		if !bytes.Equal(webhookPayload.Token, webhookToken) {
			t.Errorf("webhook token mismatch: got %v, want %v", webhookPayload.Token, webhookToken)
		}
		if webhookPayload.Error != nil {
			t.Errorf("webhook should not have error, got: %s", *webhookPayload.Error)
		}

		t.Logf("Webhook received successfully with correct payload")
	})

	// Sub-test: Transcode with heartbeat webhook
	t.Run("with heartbeat webhook", func(t *testing.T) {
		// Clear previous MockServer recordings
		clearMockServerRecordings(t, mockServerURL)

		// Set up MockServer expectation for heartbeat webhook
		setupMockServerExpectation(t, mockServerURL, "/heartbeat")

		// Create transcode job with heartbeat webhook
		jobUUID := uuid.New()
		sourcePath := "/nas/media/testdata_sample_640x360.mkv"
		destPath := "/nas/media/output_heartbeat.mp4"
		heartbeatWebhookURI := "http://mockserver:1080/heartbeat"
		webhookToken := []byte("test-heartbeat-token")

		createResp, err := client.CreateTranscodeWithResponse(ctx, vtrest.CreateTranscodeJSONRequestBody{
			Uuid:                jobUUID,
			SourcePath:          sourcePath,
			DestinationPath:     destPath,
			Profile:             "preview",
			HeartbeatWebhookUri: &heartbeatWebhookURI,
			WebhookToken:        webhookToken,
		})
		if err != nil {
			t.Fatalf("failed to create transcode job: %v", err)
		}
		if createResp.JSON201 == nil {
			t.Fatalf("expected 201 response, got status %d: %s", createResp.StatusCode(), string(createResp.Body))
		}

		t.Logf("Created transcode job with UUID: %s and heartbeat webhook URI: %s", jobUUID, heartbeatWebhookURI)

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
			t.Logf("Job status: %s, progress: %.2f%%", job.Status, job.Progress)

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
		outputFile := filepath.Join(tempDir, "output_heartbeat.mp4")
		if _, err := os.Stat(outputFile); os.IsNotExist(err) {
			t.Fatalf("output file does not exist: %s", outputFile)
		}

		t.Logf("Transcode completed successfully, output file exists at: %s", outputFile)

		// Verify heartbeat webhooks were called - check for any heartbeat with progress
		heartbeatPayloads := getAllWebhookPayloads(t, mockServerURL, "/heartbeat")
		if len(heartbeatPayloads) == 0 {
			t.Fatalf("no heartbeat webhooks were received")
		}

		t.Logf("Received %d heartbeat webhook(s)", len(heartbeatPayloads))

		// Verify at least one heartbeat had progress info
		foundProgress := false
		for _, payload := range heartbeatPayloads {
			if payload.UUID != jobUUID {
				t.Errorf("heartbeat UUID mismatch: got %s, want %s", payload.UUID, jobUUID)
			}
			if !bytes.Equal(payload.Token, webhookToken) {
				t.Errorf("heartbeat token mismatch: got %v, want %v", payload.Token, webhookToken)
			}
			if payload.Progress != nil {
				foundProgress = true
				t.Logf("Heartbeat webhook received with progress: %.2f%%", *payload.Progress)
			}
		}

		if !foundProgress {
			t.Errorf("expected at least one heartbeat webhook with progress, but none had progress field")
		}

		t.Logf("Heartbeat webhooks received successfully with progress updates")
	})
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

// WebhookPayload matches the payload sent by WebhookWorker
type WebhookPayload struct {
	Token    []byte    `json:"token,omitempty"`
	UUID     uuid.UUID `json:"uuid"`
	Error    *string   `json:"error,omitempty"`
	Progress *float64  `json:"progress,omitempty"`
}

// setupMockServerExpectation configures MockServer to accept POST requests
func setupMockServerExpectation(t *testing.T, mockServerURL, path string) {
	expectation := map[string]interface{}{
		"httpRequest": map[string]interface{}{
			"method": "POST",
			"path":   path,
		},
		"httpResponse": map[string]interface{}{
			"statusCode": 200,
		},
	}

	body, _ := json.Marshal(expectation)
	req, err := http.NewRequest(http.MethodPut, mockServerURL+"/mockserver/expectation", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("failed to create mockserver expectation request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("failed to set up mockserver expectation: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 201 {
		respBody, _ := io.ReadAll(resp.Body)
		t.Fatalf("failed to set up mockserver expectation, status %d: %s", resp.StatusCode, string(respBody))
	}
}

// waitForWebhook polls MockServer for received requests until one is found or timeout
func waitForWebhook(t *testing.T, ctx context.Context, mockServerURL, path string, timeout time.Duration) *WebhookPayload {
	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return nil
		default:
		}

		payload, found := checkForWebhook(t, mockServerURL, path)
		if found {
			return payload
		}
		time.Sleep(500 * time.Millisecond)
	}

	return nil
}

// checkForWebhook queries MockServer for recorded requests
func checkForWebhook(t *testing.T, mockServerURL, path string) (*WebhookPayload, bool) {
	reqBody := map[string]interface{}{
		"path": path,
	}
	body, _ := json.Marshal(reqBody)

	req, err := http.NewRequest(http.MethodPut, mockServerURL+"/mockserver/retrieve?type=REQUESTS", bytes.NewReader(body))
	if err != nil {
		t.Logf("failed to create retrieve request: %v", err)
		return nil, false
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Logf("failed to retrieve mockserver requests: %v", err)
		return nil, false
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		respBody, _ := io.ReadAll(resp.Body)
		t.Logf("mockserver retrieve returned status %d: %s", resp.StatusCode, respBody)
		return nil, false
	}

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Logf("failed to read mockserver response: %v", err)
		return nil, false
	}

	// MockServer returns an array of recorded requests with nested body structure
	var requests []struct {
		Body struct {
			Type   string          `json:"type"`
			Json   json.RawMessage `json:"json"`
			String string          `json:"string"`
		} `json:"body"`
	}

	if err := json.Unmarshal(respBody, &requests); err != nil {
		t.Logf("failed to parse mockserver response: %v, body: %s", err, respBody)
		return nil, false
	}

	if len(requests) == 0 {
		return nil, false
	}

	// Try parsing from json field first, then string field
	var payload WebhookPayload
	bodyData := requests[0].Body.Json
	if len(bodyData) == 0 && requests[0].Body.String != "" {
		bodyData = []byte(requests[0].Body.String)
	}

	if len(bodyData) == 0 {
		t.Logf("no body data found in request")
		return nil, false
	}

	if err := json.Unmarshal(bodyData, &payload); err != nil {
		t.Logf("failed to parse webhook payload: %v, body: %s", err, bodyData)
		return nil, false
	}

	return &payload, true
}

// clearMockServerRecordings clears all recorded requests from MockServer
func clearMockServerRecordings(t *testing.T, mockServerURL string) {
	req, err := http.NewRequest(http.MethodPut, mockServerURL+"/mockserver/reset", nil)
	if err != nil {
		t.Logf("failed to create reset request: %v", err)
		return
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Logf("failed to reset mockserver: %v", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		respBody, _ := io.ReadAll(resp.Body)
		t.Logf("mockserver reset returned status %d: %s", resp.StatusCode, respBody)
	}
}

// getAllWebhookPayloads retrieves all recorded webhook payloads from MockServer for a given path
func getAllWebhookPayloads(t *testing.T, mockServerURL, path string) []*WebhookPayload {
	reqBody := map[string]interface{}{
		"path": path,
	}
	body, _ := json.Marshal(reqBody)

	req, err := http.NewRequest(http.MethodPut, mockServerURL+"/mockserver/retrieve?type=REQUESTS", bytes.NewReader(body))
	if err != nil {
		t.Logf("failed to create retrieve request: %v", err)
		return nil
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Logf("failed to retrieve mockserver requests: %v", err)
		return nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		respBody, _ := io.ReadAll(resp.Body)
		t.Logf("mockserver retrieve returned status %d: %s", resp.StatusCode, respBody)
		return nil
	}

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Logf("failed to read mockserver response: %v", err)
		return nil
	}

	// MockServer returns an array of recorded requests with nested body structure
	var requests []struct {
		Body struct {
			Type   string          `json:"type"`
			Json   json.RawMessage `json:"json"`
			String string          `json:"string"`
		} `json:"body"`
	}

	if err := json.Unmarshal(respBody, &requests); err != nil {
		t.Logf("failed to parse mockserver response: %v, body: %s", err, respBody)
		return nil
	}

	var payloads []*WebhookPayload
	for _, request := range requests {
		bodyData := request.Body.Json
		if len(bodyData) == 0 && request.Body.String != "" {
			bodyData = []byte(request.Body.String)
		}

		if len(bodyData) == 0 {
			continue
		}

		var payload WebhookPayload
		if err := json.Unmarshal(bodyData, &payload); err != nil {
			t.Logf("failed to parse webhook payload: %v, body: %s", err, bodyData)
			continue
		}

		payloads = append(payloads, &payload)
	}

	return payloads
}
