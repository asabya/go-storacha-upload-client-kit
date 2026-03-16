//go:build integration

package go_storacha_upload_client_kit

import (
	"bytes"
	"context"
	"crypto/rand"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	uploadcap "github.com/storacha/go-libstoracha/capabilities/upload"
	"github.com/storacha/go-ucanto/did"
	"github.com/storacha/smelt/pkg/stack"
)

// testEnv holds shared state for all integration subtests.
type testEnv struct {
	storePath string
	spaceDID  did.DID
}

// NOTE: Do not use t.Parallel() in any subtest — shared stack requires sequential execution.
func TestIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration tests in short mode")
	}

	ctx := context.Background()

	// 1. Start the smelt stack
	t.Log("Starting smelt stack...")
	s := stack.MustNewStack(t,
		stack.WithTimeout(5*time.Minute),
		stack.WithKeepOnFailure(),
		stack.WithGuppyImage("forreststoracha/guppy:dev"),
	)
	t.Log("Smelt stack started")

	// Give piri time to complete registration with delegator
	t.Log("Waiting for piri registration to complete...")
	time.Sleep(10 * time.Second)

	// Check piri logs for registration status
	logs, err := s.Logs(ctx, "piri")
	if err == nil {
		for _, line := range strings.Split(logs, "\n") {
			if strings.Contains(line, "register") || strings.Contains(line, "Register") ||
				strings.Contains(line, "provider") || strings.Contains(line, "init") {
				t.Logf("piri: %s", strings.TrimSpace(line))
			}
		}
	}

	// 2. Set env vars for the client kit to connect to local services
	t.Setenv("STORACHA_SERVICE_URL", "http://localhost:8080")
	t.Setenv("STORACHA_RECEIPTS_URL", "http://localhost:8080/receipt")
	t.Setenv("STORACHA_INDEXING_SERVICE_URL", "http://localhost:9000")

	// 3. Set service DIDs — smelt services identify as did:web:<name>
	t.Setenv("STORACHA_SERVICE_DID", "did:web:upload")
	t.Setenv("STORACHA_INDEXING_SERVICE_DID", "did:web:indexer")

	// 4. Use guppy inside the container to login and create a space,
	//    then copy the store to the host for the client kit to use.
	env := setupCredentialsViaGuppy(t, ctx, s)

	// 5. Run subtests
	t.Run("UploadFile", func(t *testing.T) { testUploadFile(t, ctx, env) })
	t.Run("UploadDirectory", func(t *testing.T) { testUploadDirectory(t, ctx, env) })
	t.Run("UploadList", func(t *testing.T) { testUploadList(t, ctx, env) })
	t.Run("DownloadViaIndexer", func(t *testing.T) { testDownloadViaIndexer(t, ctx, env) })
	t.Run("DownloadViaGateway", func(t *testing.T) { testDownloadViaGateway(t, ctx, env) })
	t.Run("LargeFileUpload", func(t *testing.T) { testLargeFileUpload(t, ctx, env) })
}

// setupCredentialsViaGuppy uses the guppy CLI inside the container to login
// and create a space, then copies the guppy store to the host.
func setupCredentialsViaGuppy(t *testing.T, ctx context.Context, s *stack.Stack) *testEnv {
	t.Helper()

	// Login via guppy inside the container
	t.Log("Logging in via guppy container...")
	stdout, stderr, err := s.Exec(ctx, "guppy", "/usr/bin/guppy", "login", "test@test.example.com")
	if err != nil {
		t.Fatalf("guppy login failed: %v\nstdout: %s\nstderr: %s", err, stdout, stderr)
	}
	t.Logf("guppy login: %s", strings.TrimSpace(stdout))

	// Generate a space
	t.Log("Generating space via guppy...")
	stdout, stderr, err = s.Exec(ctx, "guppy", "/usr/bin/guppy", "space", "generate")
	if err != nil {
		t.Fatalf("guppy space generate failed: %v\nstdout: %s\nstderr: %s", err, stdout, stderr)
	}
	spaceDIDStr := strings.TrimSpace(stdout)
	if !strings.HasPrefix(spaceDIDStr, "did:") {
		for _, line := range strings.Split(spaceDIDStr, "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "did:") {
				spaceDIDStr = line
				break
			}
		}
	}
	t.Logf("Space DID: %s", spaceDIDStr)

	spaceDID, err := did.Parse(spaceDIDStr)
	if err != nil {
		t.Fatalf("Failed to parse space DID %q: %v", spaceDIDStr, err)
	}

	// Provision the space with the account
	t.Log("Provisioning space...")
	stdout, stderr, err = s.Exec(ctx, "guppy", "/usr/bin/guppy", "space", "provision", spaceDIDStr, "test@test.example.com")
	if err != nil {
		t.Fatalf("guppy space provision failed: %v\nstdout: %s\nstderr: %s", err, stdout, stderr)
	}
	t.Logf("Space provisioned: %s", strings.TrimSpace(stdout))

	// Copy the guppy store from the container to the host
	storePath := t.TempDir()
	copyGuppyStore(t, s, storePath)

	return &testEnv{
		storePath: storePath,
		spaceDID:  spaceDID,
	}
}

// copyGuppyStore copies the guppy agent store from the container to a local directory.
func copyGuppyStore(t *testing.T, s *stack.Stack, destDir string) {
	t.Helper()

	// Get the container ID for the guppy service
	ctx := context.Background()

	// List files in the guppy store
	stdout, _, err := s.Exec(ctx, "guppy", "ls", "-la", "/root/.storacha/guppy/")
	if err != nil {
		t.Fatalf("Failed to list guppy store: %v", err)
	}
	t.Logf("Guppy store contents: %s", stdout)

	// Use docker cp to copy the store directory
	// First find the container name/id
	cmd := exec.Command("docker", "ps", "--filter", "label=com.docker.compose.service=guppy",
		"--format", "{{.ID}}")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("Failed to find guppy container: %v\n%s", err, string(out))
	}
	containerID := strings.TrimSpace(string(out))
	if containerID == "" {
		t.Fatal("No guppy container found")
	}

	// Copy the store
	cmd = exec.Command("docker", "cp", containerID+":/root/.storacha/guppy/.", destDir)
	out, err = cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("Failed to copy guppy store: %v\n%s", err, string(out))
	}
	t.Logf("Copied guppy store to %s", destDir)

	// Verify the copy
	entries, _ := os.ReadDir(destDir)
	for _, e := range entries {
		t.Logf("  store file: %s", e.Name())
	}
}

// dockerHostMap maps Docker internal hostnames to host-accessible addresses.
// Smelt exposes: piri:3000 → localhost:4000, upload:80 → localhost:8080, indexer:80 → localhost:9000
var dockerHostMap = map[string]string{
	"piri:3000":    "localhost:4000",
	"piri:4000":    "localhost:4000",
	"upload:80":    "localhost:8080",
	"indexer:80":   "localhost:9000",
}

// dockerRewriteTransport rewrites Docker-internal URLs to host-accessible ones.
type dockerRewriteTransport struct {
	base http.RoundTripper
}

func (t *dockerRewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	hostPort := req.URL.Host
	if mapped, ok := dockerHostMap[hostPort]; ok {
		req = req.Clone(req.Context())
		req.URL.Host = mapped
		req.Host = mapped
	}
	return t.base.RoundTrip(req)
}

// newClientFromStore creates a StorachaClient using the copied guppy store,
// with a URL-rewriting HTTP transport for Docker internal hostnames.
func newClientFromStore(t *testing.T, env *testEnv) *StorachaClient {
	t.Helper()
	client, err := NewStorachaClient(env.storePath)
	if err != nil {
		t.Fatalf("Failed to create client: %v", err)
	}

	// Replace the put client with one that rewrites Docker internal URLs
	rewriter := &dockerRewriteTransport{base: &http.Transport{DisableCompression: true}}
	client.putClient = &http.Client{Transport: rewriter}

	return client
}

// ---------------------------------------------------------------------------
// Test functions
// ---------------------------------------------------------------------------

func testUploadFile(t *testing.T, ctx context.Context, env *testEnv) {
	client := newClientFromStore(t, env)
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "test.txt")
	os.WriteFile(testFile, []byte("Hello, Storacha integration test!"), 0644)

	result, err := client.UploadFile(ctx, env.spaceDID, testFile, &UploadOptions{Wrap: true})
	if err != nil {
		t.Fatalf("UploadFile failed: %v", err)
	}
	if !result.RootCID.Defined() {
		t.Error("Expected a defined root CID")
	}
	if result.URL == "" {
		t.Error("Expected a non-empty URL")
	}
	t.Logf("Upload succeeded: CID=%s URL=%s", result.RootCID, result.URL)
}

func testUploadDirectory(t *testing.T, ctx context.Context, env *testEnv) {
	client := newClientFromStore(t, env)
	tmpDir := t.TempDir()
	testDir := filepath.Join(tmpDir, "testdir")
	for path, content := range map[string]string{
		"file1.txt": "Content of file 1", "file2.txt": "Content of file 2",
		"subdir/file3.txt": "Content of file 3 in subdirectory",
	} {
		fullPath := filepath.Join(testDir, path)
		os.MkdirAll(filepath.Dir(fullPath), 0755)
		os.WriteFile(fullPath, []byte(content), 0644)
	}
	result, err := client.UploadDirectory(ctx, env.spaceDID, testDir, &UploadOptions{Wrap: true})
	if err != nil {
		t.Fatalf("UploadDirectory failed: %v", err)
	}
	if !result.RootCID.Defined() {
		t.Error("Expected a defined root CID")
	}
	t.Logf("Directory upload succeeded: CID=%s", result.RootCID)
}

func testUploadList(t *testing.T, ctx context.Context, env *testEnv) {
	client := newClientFromStore(t, env)
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "list-test.txt")
	os.WriteFile(testFile, []byte("Upload list test content"), 0644)

	uploadResult, err := client.UploadFile(ctx, env.spaceDID, testFile, &UploadOptions{Wrap: true})
	if err != nil {
		t.Fatalf("UploadFile failed: %v", err)
	}
	listResult, err := client.UploadList(ctx, env.spaceDID, uploadcap.ListCaveats{})
	if err != nil {
		if strings.Contains(err.Error(), "does not implement") {
			t.Skipf("upload/list not implemented by local upload service: %v", err)
		}
		t.Fatalf("UploadList failed: %v", err)
	}
	found := false
	for _, item := range listResult.Results {
		if item.Root.String() == uploadResult.RootCID.String() {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("Uploaded CID %s not found in upload list (%d items)", uploadResult.RootCID, len(listResult.Results))
	}
	t.Logf("UploadList succeeded: %d uploads, target found=%v", len(listResult.Results), found)
}

func testDownloadViaIndexer(t *testing.T, ctx context.Context, env *testEnv) {
	client := newClientFromStore(t, env)
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "indexer-test.txt")
	testContent := []byte("Download via indexer test content - verifying round-trip integrity")
	os.WriteFile(testFile, testContent, 0644)

	// Wrap: false so root CID is a file node (DownloadFileViaIndexer expects this).
	uploadResult, err := client.UploadFile(ctx, env.spaceDID, testFile, &UploadOptions{Wrap: false})
	if err != nil {
		t.Fatalf("UploadFile failed: %v", err)
	}
	outputPath := filepath.Join(tmpDir, "downloaded.txt")
	var downloadErr error
	for attempt := 1; attempt <= 6; attempt++ {
		t.Logf("Download attempt %d/6...", attempt)
		downloadErr = client.DownloadFileViaIndexer(ctx, env.spaceDID, uploadResult.RootCID, outputPath)
		if downloadErr == nil {
			break
		}
		t.Logf("Attempt %d failed: %v", attempt, downloadErr)
		time.Sleep(5 * time.Second)
	}
	if downloadErr != nil {
		t.Fatalf("DownloadFileViaIndexer failed after retries: %v", downloadErr)
	}
	downloaded, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("Failed to read downloaded file: %v", err)
	}
	if string(downloaded) != string(testContent) {
		t.Errorf("Content mismatch:\n  got:  %q\n  want: %q", string(downloaded), string(testContent))
	}
	t.Logf("Download via indexer succeeded: %d bytes match", len(downloaded))
}

func testDownloadViaGateway(t *testing.T, ctx context.Context, env *testEnv) {
	t.Skip("Gateway download not available in local smelt stack - requires IPFS gateway")
}

func testLargeFileUpload(t *testing.T, ctx context.Context, env *testEnv) {
	client := newClientFromStore(t, env)
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "large-test.bin")
	fileSize := 5 * 1024 * 1024
	testContent := make([]byte, fileSize)
	rand.Read(testContent)
	os.WriteFile(testFile, testContent, 0644)

	t.Logf("Uploading %d byte file...", fileSize)
	uploadResult, err := client.UploadFile(ctx, env.spaceDID, testFile, &UploadOptions{
		Wrap: false,
		OnProgress: func(uploaded int64) {
			t.Logf("Upload progress: %d / %d bytes", uploaded, fileSize)
		},
	})
	if err != nil {
		t.Fatalf("Large file upload failed: %v", err)
	}
	if !uploadResult.RootCID.Defined() {
		t.Error("Expected a defined root CID")
	}
	outputPath := filepath.Join(tmpDir, "downloaded-large.bin")
	var downloadErr error
	for attempt := 1; attempt <= 6; attempt++ {
		t.Logf("Download attempt %d/6...", attempt)
		downloadErr = client.DownloadFileViaIndexer(ctx, env.spaceDID, uploadResult.RootCID, outputPath)
		if downloadErr == nil {
			break
		}
		t.Logf("Attempt %d failed: %v", attempt, downloadErr)
		time.Sleep(5 * time.Second)
	}
	if downloadErr != nil {
		t.Fatalf("Large file download failed after retries: %v", downloadErr)
	}
	downloaded, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("Failed to read downloaded file: %v", err)
	}
	if !bytes.Equal(downloaded, testContent) {
		t.Fatalf("Content mismatch: got %d bytes, want %d bytes", len(downloaded), len(testContent))
	}
	t.Logf("Large file round-trip succeeded: %d bytes match", len(downloaded))
}
