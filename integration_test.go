//go:build integration

package go_storacha_upload_client_kit

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	uploadcap "github.com/storacha/go-libstoracha/capabilities/upload"
	"github.com/storacha/go-ucanto/core/delegation"
	"github.com/storacha/go-ucanto/did"
	"github.com/storacha/go-ucanto/principal"
	ed25519signer "github.com/storacha/go-ucanto/principal/ed25519/signer"
	"github.com/storacha/smelt/pkg/stack"
)

// testEnv holds shared state for all integration subtests.
type testEnv struct {
	signer      principal.Signer
	delegations []delegation.Delegation
	spaceDID    did.DID
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
	)
	t.Log("Smelt stack started")

	// 2. Set env vars for the client kit to connect to local services
	t.Setenv("STORACHA_SERVICE_URL", "http://localhost:8080")
	t.Setenv("STORACHA_RECEIPTS_URL", "http://localhost:8080/receipt")
	t.Setenv("STORACHA_INDEXING_SERVICE_URL", "http://localhost:9000")

	// 3. Discover service DIDs from the running containers
	serviceDID := discoverServiceDID(t, ctx, s, "upload")
	indexerDID := discoverServiceDID(t, ctx, s, "indexer")
	t.Setenv("STORACHA_SERVICE_DID", serviceDID)
	t.Setenv("STORACHA_INDEXING_SERVICE_DID", indexerDID)

	// 4. Login to get credentials for subsequent tests
	env := setupCredentials(t, ctx)

	// 5. Run subtests
	t.Run("Login", func(t *testing.T) { testLogin(t, ctx) })
	t.Run("UploadFile", func(t *testing.T) { testUploadFile(t, ctx, env) })
	t.Run("UploadDirectory", func(t *testing.T) { testUploadDirectory(t, ctx, env) })
	t.Run("UploadList", func(t *testing.T) { testUploadList(t, ctx, env) })
	t.Run("DownloadViaIndexer", func(t *testing.T) { testDownloadViaIndexer(t, ctx, env) })
	t.Run("DownloadViaGateway", func(t *testing.T) { testDownloadViaGateway(t, ctx, env) })
	t.Run("LargeFileUpload", func(t *testing.T) { testLargeFileUpload(t, ctx, env) })
}

// discoverServiceDID reads the service's PEM key from inside the container
// and derives the did:key: identifier.
func discoverServiceDID(t *testing.T, ctx context.Context, s *stack.Stack, service string) string {
	t.Helper()

	// Try reading the PEM from the container's key mount
	keyPath := "/keys/" + service + ".pem"
	stdout, stderr, err := s.Exec(ctx, service, "cat", keyPath)
	if err != nil {
		t.Logf("Could not read key from %s:%s (stderr: %s), will try did:web:%s", service, keyPath, stderr, service)
		return "did:web:" + service
	}

	// Parse the PEM to derive did:key:
	didStr, err := didFromPEM(stdout)
	if err != nil {
		t.Logf("Could not derive DID from PEM for %s: %v, falling back to did:web:%s", service, err, service)
		return "did:web:" + service
	}

	t.Logf("Discovered %s DID: %s", service, didStr)
	return didStr
}

// didFromPEM parses a PKCS#8 PEM-encoded Ed25519 private key and returns
// the corresponding did:key: string.
func didFromPEM(pemData string) (string, error) {
	s, err := parseEd25519PEM([]byte(pemData))
	if err != nil {
		return "", err
	}
	return s.DID().String(), nil
}

// parseEd25519PEM parses a PEM-encoded Ed25519 private key (PKCS#8 format)
// and returns a go-ucanto signer.
func parseEd25519PEM(pemBytes []byte) (principal.Signer, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, fmt.Errorf("no PEM block found")
	}

	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parsing PKCS#8 key: %w", err)
	}

	edKey, ok := key.(ed25519.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("key is not Ed25519")
	}

	return ed25519signer.FromRaw(edKey)
}

// setupCredentials performs a login flow and caches the results for subtests.
func setupCredentials(t *testing.T, ctx context.Context) *testEnv {
	t.Helper()

	tmpDir := t.TempDir()

	client, err := NewStorachaClient(tmpDir)
	if err != nil {
		t.Fatalf("Failed to create client for setup: %v", err)
	}

	// Generate a new signer
	s, err := GenerateSigner()
	if err != nil {
		t.Fatalf("Failed to generate signer: %v", err)
	}
	if err := client.SetPrincipal(s); err != nil {
		t.Fatalf("Failed to set principal: %v", err)
	}

	// Login
	t.Log("Logging in to local stack...")
	result, err := client.LoginAndSave(ctx, "test@test.example.com", nil)
	if err != nil {
		t.Fatalf("Login failed during setup: %v", err)
	}
	t.Logf("Login succeeded, got %d delegations", len(result.Delegations))

	// Get spaces
	spaces, err := client.Spaces()
	if err != nil {
		t.Fatalf("Failed to get spaces: %v", err)
	}
	if len(spaces) == 0 {
		t.Fatal("No spaces available after login")
	}
	t.Logf("Using space: %s", spaces[0].String())

	return &testEnv{
		signer:      s,
		delegations: result.Delegations,
		spaceDID:    spaces[0],
	}
}

// newClientFromEnv creates a StorachaClient using cached credentials from testEnv.
func newClientFromEnv(t *testing.T, env *testEnv) *StorachaClient {
	t.Helper()

	tmpDir := t.TempDir()
	client, err := NewStorachaClient(tmpDir)
	if err != nil {
		t.Fatalf("Failed to create client: %v", err)
	}

	if err := client.SetPrincipal(env.signer); err != nil {
		t.Fatalf("Failed to set principal: %v", err)
	}

	if err := client.SaveDelegations(env.delegations...); err != nil {
		t.Fatalf("Failed to save delegations: %v", err)
	}

	return client
}

func testLogin(t *testing.T, ctx context.Context) {
	tmpDir := t.TempDir()

	client, err := NewStorachaClient(tmpDir)
	if err != nil {
		t.Fatalf("Failed to create client: %v", err)
	}

	s, err := GenerateSigner()
	if err != nil {
		t.Fatalf("Failed to generate signer: %v", err)
	}
	if err := client.SetPrincipal(s); err != nil {
		t.Fatalf("Failed to set principal: %v", err)
	}

	result, err := client.LoginAndSave(ctx, "logintest@test.example.com", nil)
	if err != nil {
		t.Fatalf("Login failed: %v", err)
	}

	if result.AccountDID.String() == "" {
		t.Error("Expected non-empty AccountDID")
	}
	if len(result.Delegations) == 0 {
		t.Error("Expected at least one delegation")
	}

	spaces, err := client.Spaces()
	if err != nil {
		t.Fatalf("Failed to get spaces: %v", err)
	}
	if len(spaces) == 0 {
		t.Error("Expected at least one space after login")
	}

	t.Logf("Login succeeded: account=%s, delegations=%d, spaces=%d",
		result.AccountDID, len(result.Delegations), len(spaces))
}

func testUploadFile(t *testing.T, ctx context.Context, env *testEnv) {
	client := newClientFromEnv(t, env)

	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "test.txt")
	testContent := []byte("Hello, Storacha integration test!")
	if err := os.WriteFile(testFile, testContent, 0644); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

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
	client := newClientFromEnv(t, env)

	tmpDir := t.TempDir()
	testDir := filepath.Join(tmpDir, "testdir")

	files := map[string]string{
		"file1.txt":        "Content of file 1",
		"file2.txt":        "Content of file 2",
		"subdir/file3.txt": "Content of file 3 in subdirectory",
	}

	for path, content := range files {
		fullPath := filepath.Join(testDir, path)
		if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
			t.Fatalf("Failed to create directory: %v", err)
		}
		if err := os.WriteFile(fullPath, []byte(content), 0644); err != nil {
			t.Fatalf("Failed to create test file %s: %v", path, err)
		}
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
	client := newClientFromEnv(t, env)

	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "list-test.txt")
	if err := os.WriteFile(testFile, []byte("Upload list test content"), 0644); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	uploadResult, err := client.UploadFile(ctx, env.spaceDID, testFile, &UploadOptions{Wrap: true})
	if err != nil {
		t.Fatalf("UploadFile failed: %v", err)
	}

	listResult, err := client.UploadList(ctx, env.spaceDID, uploadcap.ListCaveats{})
	if err != nil {
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
		t.Errorf("Uploaded CID %s not found in upload list (got %d items)",
			uploadResult.RootCID, len(listResult.Results))
	}

	t.Logf("UploadList succeeded: found %d uploads, target CID found=%v",
		len(listResult.Results), found)
}

func testDownloadViaIndexer(t *testing.T, ctx context.Context, env *testEnv) {
	client := newClientFromEnv(t, env)

	// IMPORTANT: Use Wrap: false so the root CID points to the file itself,
	// not a wrapping directory. DownloadFileViaIndexer expects a file node.
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "indexer-test.txt")
	testContent := []byte("Download via indexer test content - verifying round-trip integrity")
	if err := os.WriteFile(testFile, testContent, 0644); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	uploadResult, err := client.UploadFile(ctx, env.spaceDID, testFile, &UploadOptions{Wrap: false})
	if err != nil {
		t.Fatalf("UploadFile failed: %v", err)
	}

	// Wait for indexer propagation with retry
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
	client := newClientFromEnv(t, env)

	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "large-test.bin")
	fileSize := 5 * 1024 * 1024 // 5 MiB

	testContent := make([]byte, fileSize)
	if _, err := rand.Read(testContent); err != nil {
		t.Fatalf("Failed to generate random data: %v", err)
	}
	if err := os.WriteFile(testFile, testContent, 0644); err != nil {
		t.Fatalf("Failed to create large test file: %v", err)
	}

	// IMPORTANT: Use Wrap: false so root CID is a file node for download
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
