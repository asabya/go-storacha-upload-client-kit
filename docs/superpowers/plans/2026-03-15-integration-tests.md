# Integration Tests Implementation Plan

> **For agentic workers:** REQUIRED: Use superpowers:subagent-driven-development (if subagents available) or superpowers:executing-plans to implement this plan. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add end-to-end integration tests that verify upload, download, list, and login flows against a local Storacha network orchestrated by smelt.

**Architecture:** A single `integration_test.go` file gated by `//go:build integration`. One parent `TestIntegration` function starts a smelt stack, performs login to cache credentials, then runs 7 subtests via `t.Run()`. The stack is shared across all subtests and cleaned up when the parent test completes.

**Tech Stack:** Go testing, smelt (`github.com/storacha/smelt/pkg/stack`), testcontainers-go (via smelt), Docker Compose

**Spec:** `docs/superpowers/specs/2026-03-15-integration-tests-design.md`

---

## Chunk 1: Dependency & Scaffolding

### Task 1: Add smelt dependency

**Files:**
- Modify: `go.mod`

- [ ] **Step 1: Add smelt dependency**

```bash
cd /Users/sabyasachipatra/go/src/github.com/asabya/go-storacha-upload-client-kit
go get github.com/storacha/smelt@latest
```

- [ ] **Step 2: Tidy modules**

```bash
go mod tidy
```

- [ ] **Step 3: Verify build**

```bash
go build ./...
```

Expected: no errors

- [ ] **Step 4: Commit**

```bash
git add go.mod go.sum
git commit -m "chore: add smelt dependency for integration tests"
```

---

### Task 2: Create integration test file with build tag and parent test scaffold

**Files:**
- Create: `integration_test.go`

- [ ] **Step 1: Create the integration test file with build tag, imports, and parent test**

Write `integration_test.go`:

```go
//go:build integration

package go_storacha_upload_client_kit

import (
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	uploadcap "github.com/storacha/go-libstoracha/capabilities/upload"
	"github.com/storacha/go-ucanto/core/delegation"
	"github.com/storacha/go-ucanto/did"
	"github.com/storacha/go-ucanto/principal"
	"github.com/storacha/smelt/pkg/stack"
)

// testEnv holds shared state for all integration subtests.
type testEnv struct {
	signer      principal.Signer
	delegations []delegation.Delegation
	spaceDID    did.DID
}

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
		// Fall back to did:web:<service>
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
	// Use go-ucanto's ed25519 signer to parse the PEM
	// This handles PKCS#8 format that smelt generates
	s, err := parseEd25519PEM([]byte(pemData))
	if err != nil {
		return "", err
	}
	return s.DID().String(), nil
}

// parseEd25519PEM parses a PEM-encoded Ed25519 private key.
// This is implemented in Task 3.
func parseEd25519PEM(pemBytes []byte) (principal.Signer, error) {
	// Placeholder - implemented in Task 3
	return nil, fmt.Errorf("not implemented")
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

// Stub test functions - implemented in subsequent tasks
func testLogin(t *testing.T, ctx context.Context)                          { t.Skip("not yet implemented") }
func testUploadFile(t *testing.T, ctx context.Context, env *testEnv)       { t.Skip("not yet implemented") }
func testUploadDirectory(t *testing.T, ctx context.Context, env *testEnv)  { t.Skip("not yet implemented") }
func testUploadList(t *testing.T, ctx context.Context, env *testEnv)       { t.Skip("not yet implemented") }
func testDownloadViaIndexer(t *testing.T, ctx context.Context, env *testEnv) { t.Skip("not yet implemented") }
func testDownloadViaGateway(t *testing.T, ctx context.Context, env *testEnv) { t.Skip("not yet implemented") }
func testLargeFileUpload(t *testing.T, ctx context.Context, env *testEnv)  { t.Skip("not yet implemented") }
```

- [ ] **Step 2: Verify it compiles with integration tag**

```bash
go build -tags integration ./...
```

Expected: no errors (will have unused import warnings — fix as needed)

- [ ] **Step 3: Verify unit tests still pass without the tag**

```bash
go test -v -run TestParseSizeUtil ./...
```

Expected: PASS (integration tests not included)

- [ ] **Step 4: Commit**

```bash
git add integration_test.go
git commit -m "feat: scaffold integration test with smelt stack lifecycle"
```

---

### Task 3: Implement PEM parsing helper

**Files:**
- Modify: `integration_test.go`

- [ ] **Step 1: Replace the `parseEd25519PEM` placeholder with real implementation**

Replace the placeholder `parseEd25519PEM` function in `integration_test.go`:

```go
import (
	"crypto/ed25519"
	"crypto/x509"
	"encoding/pem"
	"fmt"

	ed25519signer "github.com/storacha/go-ucanto/principal/ed25519/signer"
)

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
```

- [ ] **Step 2: Verify it compiles**

```bash
go build -tags integration ./...
```

Expected: no errors

- [ ] **Step 3: Commit**

```bash
git add integration_test.go
git commit -m "feat: implement PEM parsing for DID discovery in integration tests"
```

---

## Chunk 2: Test Implementations

### Task 4: Implement testLogin

**Files:**
- Modify: `integration_test.go`

- [ ] **Step 1: Replace the `testLogin` stub**

```go
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
```

- [ ] **Step 2: Verify it compiles**

```bash
go build -tags integration ./...
```

- [ ] **Step 3: Commit**

```bash
git add integration_test.go
git commit -m "feat: implement Login integration test"
```

---

### Task 5: Implement testUploadFile

**Files:**
- Modify: `integration_test.go`

- [ ] **Step 1: Add helper to create a client from cached credentials**

```go
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
```

- [ ] **Step 2: Replace the `testUploadFile` stub**

```go
func testUploadFile(t *testing.T, ctx context.Context, env *testEnv) {
	client := newClientFromEnv(t, env)

	// Create a test file
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
```

- [ ] **Step 3: Verify it compiles**

```bash
go build -tags integration ./...
```

- [ ] **Step 4: Commit**

```bash
git add integration_test.go
git commit -m "feat: implement UploadFile integration test"
```

---

### Task 6: Implement testUploadDirectory

**Files:**
- Modify: `integration_test.go`

- [ ] **Step 1: Replace the `testUploadDirectory` stub**

```go
func testUploadDirectory(t *testing.T, ctx context.Context, env *testEnv) {
	client := newClientFromEnv(t, env)

	// Create a test directory with files and a subdirectory
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
```

- [ ] **Step 2: Verify it compiles**

```bash
go build -tags integration ./...
```

- [ ] **Step 3: Commit**

```bash
git add integration_test.go
git commit -m "feat: implement UploadDirectory integration test"
```

---

### Task 7: Implement testUploadList

**Files:**
- Modify: `integration_test.go`

- [ ] **Step 1: Replace the `testUploadList` stub** (import already in scaffold)

```go
func testUploadList(t *testing.T, ctx context.Context, env *testEnv) {
	client := newClientFromEnv(t, env)

	// Upload a file first
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "list-test.txt")
	if err := os.WriteFile(testFile, []byte("Upload list test content"), 0644); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	uploadResult, err := client.UploadFile(ctx, env.spaceDID, testFile, &UploadOptions{Wrap: true})
	if err != nil {
		t.Fatalf("UploadFile failed: %v", err)
	}

	// List uploads
	listResult, err := client.UploadList(ctx, env.spaceDID, uploadcap.ListCaveats{})
	if err != nil {
		t.Fatalf("UploadList failed: %v", err)
	}

	// Check that our upload appears in the list
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
```

- [ ] **Step 2: Verify it compiles**

```bash
go build -tags integration ./...
```

- [ ] **Step 3: Commit**

```bash
git add integration_test.go
git commit -m "feat: implement UploadList integration test"
```

---

### Task 8: Implement testDownloadViaIndexer

**Files:**
- Modify: `integration_test.go`

- [ ] **Step 1: Replace the `testDownloadViaIndexer` stub**

```go
func testDownloadViaIndexer(t *testing.T, ctx context.Context, env *testEnv) {
	client := newClientFromEnv(t, env)

	// Upload a file with known content.
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

	// Verify content matches
	downloaded, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("Failed to read downloaded file: %v", err)
	}

	if string(downloaded) != string(testContent) {
		t.Errorf("Content mismatch:\n  got:  %q\n  want: %q", string(downloaded), string(testContent))
	}

	t.Logf("Download via indexer succeeded: %d bytes match", len(downloaded))
}
```

- [ ] **Step 2: Verify it compiles**

```bash
go build -tags integration ./...
```

- [ ] **Step 3: Commit**

```bash
git add integration_test.go
git commit -m "feat: implement DownloadViaIndexer integration test"
```

---

### Task 9: Implement testDownloadViaGateway

**Files:**
- Modify: `integration_test.go`

- [ ] **Step 1: Replace the `testDownloadViaGateway` stub**

```go
func testDownloadViaGateway(t *testing.T, ctx context.Context, env *testEnv) {
	// Gateway download requires an IPFS HTTP gateway, which may not be available
	// in smelt's local stack. Skip if not available.
	t.Skip("Gateway download not available in local smelt stack - requires IPFS gateway")

	// If a gateway becomes available in the future, the test would be:
	//
	// client := newClientFromEnv(t, env)
	// tmpDir := t.TempDir()
	// testFile := filepath.Join(tmpDir, "gateway-test.txt")
	// testContent := []byte("Download via gateway test content")
	// os.WriteFile(testFile, testContent, 0644)
	//
	// uploadResult, _ := client.UploadFile(ctx, env.spaceDID, testFile, &UploadOptions{Wrap: true})
	//
	// outputPath := filepath.Join(tmpDir, "downloaded.txt")
	// err := DownloadFile(ctx, uploadResult.RootCID, outputPath, &DownloadOptions{
	//     Gateway: "localhost:XXXX",
	// })
	//
	// downloaded, _ := os.ReadFile(outputPath)
	// assert content matches
}
```

- [ ] **Step 2: Verify it compiles**

```bash
go build -tags integration ./...
```

- [ ] **Step 3: Commit**

```bash
git add integration_test.go
git commit -m "feat: implement DownloadViaGateway integration test (skipped - no local gateway)"
```

---

### Task 10: Implement testLargeFileUpload

**Files:**
- Modify: `integration_test.go`

- [ ] **Step 1: Replace the `testLargeFileUpload` stub** (imports already in scaffold)

```go
func testLargeFileUpload(t *testing.T, ctx context.Context, env *testEnv) {
	client := newClientFromEnv(t, env)

	// Generate a 5MB file to produce multiple UnixFS blocks
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

	// IMPORTANT: Use Wrap: false so the root CID points to the file itself,
	// not a wrapping directory. DownloadFileViaIndexer expects a file node.
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

	// Download via indexer and verify content
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
```

- [ ] **Step 2: Verify it compiles**

```bash
go build -tags integration ./...
```

- [ ] **Step 3: Commit**

```bash
git add integration_test.go
git commit -m "feat: implement LargeFileUpload integration test"
```

---

## Chunk 3: Verify & Finalize

### Task 11: Final compilation check and cleanup

**Files:**
- Modify: `integration_test.go` (if needed)

- [ ] **Step 1: Verify full compilation with integration tag**

```bash
go build -tags integration ./...
```

- [ ] **Step 2: Verify unit tests still pass without integration tag**

```bash
go test -v -count=1 ./...
```

Expected: all existing tests pass, integration tests not included

- [ ] **Step 3: Remove any unused imports or fix lint issues**

```bash
go vet -tags integration ./...
```

- [ ] **Step 4: Final commit if any cleanup was needed**

```bash
git add integration_test.go
git commit -m "chore: clean up integration test imports and lint"
```

---

### Task 12: Run integration tests against local stack (manual verification)

- [ ] **Step 1: Ensure Docker is running**

```bash
docker info > /dev/null 2>&1 && echo "Docker OK" || echo "Docker not running"
```

- [ ] **Step 2: Run integration tests**

```bash
cd /Users/sabyasachipatra/go/src/github.com/asabya/go-storacha-upload-client-kit
go test -tags integration -v -timeout 15m -run TestIntegration
```

Expected: All subtests pass (except DownloadViaGateway which is skipped)

- [ ] **Step 3: If any tests fail, debug using stack logs**

```
# Logs are available via t.Log output
# If using WithKeepOnFailure(), containers remain running for inspection:
docker compose -p smeltery-testintegration logs upload
docker compose -p smeltery-testintegration logs indexer
```

- [ ] **Step 4: Final commit after successful run**

```bash
git add -A
git commit -m "feat: integration tests verified against local smelt stack"
```
