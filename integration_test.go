//go:build integration

package go_storacha_upload_client_kit

import (
	"context"
	"crypto/ed25519"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"testing"
	"time"

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

// Stub test functions - implemented in subsequent tasks
func testLogin(t *testing.T, ctx context.Context)                            { t.Skip("not yet implemented") }
func testUploadFile(t *testing.T, ctx context.Context, env *testEnv)         { t.Skip("not yet implemented") }
func testUploadDirectory(t *testing.T, ctx context.Context, env *testEnv)    { t.Skip("not yet implemented") }
func testUploadList(t *testing.T, ctx context.Context, env *testEnv)         { t.Skip("not yet implemented") }
func testDownloadViaIndexer(t *testing.T, ctx context.Context, env *testEnv) { t.Skip("not yet implemented") }
func testDownloadViaGateway(t *testing.T, ctx context.Context, env *testEnv) { t.Skip("not yet implemented") }
func testLargeFileUpload(t *testing.T, ctx context.Context, env *testEnv)    { t.Skip("not yet implemented") }
