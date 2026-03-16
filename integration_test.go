//go:build integration

package go_storacha_upload_client_kit

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	uploadcap "github.com/storacha/go-libstoracha/capabilities/upload"
	"github.com/storacha/go-ucanto/core/delegation"
	"github.com/storacha/go-ucanto/did"
	"github.com/storacha/go-ucanto/principal"
	ed25519signer "github.com/storacha/go-ucanto/principal/ed25519/signer"
	ucanosigner "github.com/storacha/go-ucanto/principal/signer"
	"github.com/storacha/go-ucanto/ucan"
	"github.com/storacha/smelt"
)

// testEnv holds shared state for all integration subtests.
type testEnv struct {
	signer      principal.Signer
	delegations []delegation.Delegation
	spaceDID    did.DID
}

// smeltImages lists all container images used by the smelt stack.
var smeltImages = []string{
	"ghcr.io/storacha/filecoin-localdev:f2acf8a",
	"amazon/dynamodb-local:latest",
	"redis:7-alpine",
	"ghcr.io/storacha/delegator:main",
	"ghcr.io/storacha/indexing-service:main",
	"ghcr.io/storacha/storetheindex:main",
	"ghcr.io/storacha/piri:main",
	"ghcr.io/storacha/piri-signing-service:main",
	"ghcr.io/storacha/sprue:main",
}

// NOTE: Do not use t.Parallel() in any subtest — shared stack requires sequential execution.
func TestIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration tests in short mode")
	}

	ctx := context.Background()

	// 0. Pre-pull all images as linux/amd64.
	prePullImages(t)

	// 1. Set up smelt directory and start stack.
	tempDir := setupSmeltDir(t)
	projectName := "smeltery-kit-test"
	startSmeltStack(t, tempDir, projectName)

	// 2. Set env vars for the client kit to connect to local services.
	t.Setenv("STORACHA_SERVICE_URL", "http://localhost:8080")
	t.Setenv("STORACHA_RECEIPTS_URL", "http://localhost:8080/receipt")
	t.Setenv("STORACHA_INDEXING_SERVICE_URL", "http://localhost:9000")

	// 3. Discover service DIDs.
	serviceDID := discoverServiceDIDExec(t, projectName, "upload")
	indexerDID := discoverServiceDIDExec(t, projectName, "indexer")
	t.Setenv("STORACHA_SERVICE_DID", serviceDID)
	t.Setenv("STORACHA_INDEXING_SERVICE_DID", indexerDID)

	// 4. Login to get credentials.
	env := setupCredentials(t, ctx)

	// 5. Run subtests.
	t.Run("Login", func(t *testing.T) { testLogin(t, ctx) })
	t.Run("UploadFile", func(t *testing.T) { testUploadFile(t, ctx, env) })
	t.Run("UploadDirectory", func(t *testing.T) { testUploadDirectory(t, ctx, env) })
	t.Run("UploadList", func(t *testing.T) { testUploadList(t, ctx, env) })
	t.Run("DownloadViaIndexer", func(t *testing.T) { testDownloadViaIndexer(t, ctx, env) })
	t.Run("DownloadViaGateway", func(t *testing.T) { testDownloadViaGateway(t, ctx, env) })
	t.Run("LargeFileUpload", func(t *testing.T) { testLargeFileUpload(t, ctx, env) })
}

// ---------------------------------------------------------------------------
// Stack setup helpers
// ---------------------------------------------------------------------------

func prePullImages(t *testing.T) {
	t.Helper()
	t.Log("Pre-pulling images as linux/amd64...")
	for _, img := range smeltImages {
		cmd := exec.Command("docker", "pull", "--platform", "linux/amd64", img)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Logf("Warning: failed to pull %s: %v\n%s", img, err, string(out))
		}
	}
	t.Log("All images pre-pulled")
}

func setupSmeltDir(t *testing.T) string {
	t.Helper()

	tempDir := t.TempDir()

	// Extract embedded compose files from smelt.
	if err := extractEmbeddedFS(tempDir, "."); err != nil {
		t.Fatalf("Failed to extract smelt files: %v", err)
	}

	// Override .env to use public default images.
	envContent := `# Integration test - use public images
PIRI_IMAGE=ghcr.io/storacha/piri:main
GUPPY_IMAGE=ghcr.io/storacha/indexing-service:main
DELEGATOR_IMAGE=ghcr.io/storacha/delegator:main
INDEXER_IMAGE=ghcr.io/storacha/indexing-service:main
IPNI_IMAGE=ghcr.io/storacha/storetheindex:main
SIGNER_IMAGE=ghcr.io/storacha/piri-signing-service:main
UPLOAD_IMAGE=ghcr.io/storacha/sprue:main
BLOCKCHAIN_IMAGE=ghcr.io/storacha/filecoin-localdev:f2acf8a
`
	if err := os.WriteFile(filepath.Join(tempDir, ".env"), []byte(envContent), 0644); err != nil {
		t.Fatalf("Failed to write .env: %v", err)
	}

	// Generate Ed25519 keys for services.
	keysDir := filepath.Join(tempDir, "generated", "keys")
	os.MkdirAll(keysDir, 0755)
	for _, svc := range []string{"piri", "upload", "indexer", "delegator", "signing-service", "etracker"} {
		genEd25519Key(t, keysDir, svc)
	}

	// Extract EVM keys from deployed-addresses.json.
	genEVMKeys(t, tempDir, keysDir)

	// Generate UCAN delegation proofs.
	proofsDir := filepath.Join(tempDir, "generated", "proofs")
	os.MkdirAll(proofsDir, 0755)
	genProofs(t, keysDir, proofsDir)

	return tempDir
}

func extractEmbeddedFS(destDir, srcDir string) error {
	entries, err := smelt.EmbeddedFiles.ReadDir(srcDir)
	if err != nil {
		return fmt.Errorf("reading dir %s: %w", srcDir, err)
	}
	for _, entry := range entries {
		srcPath := filepath.Join(srcDir, entry.Name())
		destPath := filepath.Join(destDir, srcPath)
		if entry.IsDir() {
			os.MkdirAll(destPath, 0755)
			if err := extractEmbeddedFS(destDir, srcPath); err != nil {
				return err
			}
		} else {
			os.MkdirAll(filepath.Dir(destPath), 0755)
			data, err := smelt.EmbeddedFiles.ReadFile(srcPath)
			if err != nil {
				return err
			}
			mode := os.FileMode(0644)
			if strings.HasSuffix(entry.Name(), ".sh") {
				mode = 0755
			}
			if err := os.WriteFile(destPath, data, mode); err != nil {
				return err
			}
		}
	}
	return nil
}

func genEd25519Key(t *testing.T, keysDir, name string) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key for %s: %v", name, err)
	}
	pkcs8, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatalf("marshal key for %s: %v", name, err)
	}
	privPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8})
	os.WriteFile(filepath.Join(keysDir, name+".pem"), privPEM, 0600)

	pub := priv.Public().(ed25519.PublicKey)
	pubPKIX, _ := x509.MarshalPKIXPublicKey(pub)
	pubPEM := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubPKIX})
	os.WriteFile(filepath.Join(keysDir, name+".pub"), pubPEM, 0644)
}

type deployedAddresses struct {
	Deployer struct {
		PrivateKey string `json:"privateKey"`
	} `json:"deployer"`
	Payer struct {
		PrivateKey string `json:"privateKey"`
	} `json:"payer"`
}

func genEVMKeys(t *testing.T, tempDir, keysDir string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(tempDir, "systems", "blockchain", "state", "deployed-addresses.json"))
	if err != nil {
		t.Fatalf("read deployed-addresses.json: %v", err)
	}
	var addr deployedAddresses
	if err := json.Unmarshal(data, &addr); err != nil {
		t.Fatalf("parse deployed-addresses.json: %v", err)
	}

	// payer-key.hex
	payerKey := strings.TrimPrefix(addr.Payer.PrivateKey, "0x")
	os.WriteFile(filepath.Join(keysDir, "payer-key.hex"), []byte(payerKey), 0600)

	// owner-wallet.hex (piri wallet format)
	deployerKey := strings.TrimPrefix(addr.Deployer.PrivateKey, "0x")
	deployerBytes, _ := hex.DecodeString(deployerKey)
	walletJSON := fmt.Sprintf(`{"Type":"delegated","PrivateKey":"%s"}`, base64.StdEncoding.EncodeToString(deployerBytes))
	walletHex := hex.EncodeToString([]byte(walletJSON))
	os.WriteFile(filepath.Join(keysDir, "owner-wallet.hex"), []byte(walletHex), 0600)
}

func genProofs(t *testing.T, keysDir, proofsDir string) {
	t.Helper()
	type proofSpec struct {
		issuerKey, issuerDIDWeb, audienceDID, capability, outputFile string
	}
	specs := []proofSpec{
		{"indexer", "did:web:indexer", "did:web:delegator", "claim/cache", "indexing-service-proof.txt"},
		{"etracker", "did:web:etracker", "did:web:delegator", "egress/track", "egress-tracking-proof.txt"},
	}
	for _, s := range specs {
		issuerKey := loadSignerPEM(t, filepath.Join(keysDir, s.issuerKey+".pem"))
		issuerDID, _ := did.Parse(s.issuerDIDWeb)
		issuer, err := ucanosigner.Wrap(issuerKey, issuerDID)
		if err != nil {
			t.Fatalf("wrap signer: %v", err)
		}
		audience, _ := did.Parse(s.audienceDID)
		caps := []ucan.Capability[ucan.NoCaveats]{
			ucan.NewCapability(s.capability, issuer.DID().String(), ucan.NoCaveats{}),
		}
		dlg, err := delegation.Delegate(issuer, audience, caps, delegation.WithNoExpiration())
		if err != nil {
			t.Fatalf("create delegation: %v", err)
		}
		formatted, err := delegation.Format(dlg)
		if err != nil {
			t.Fatalf("format delegation: %v", err)
		}
		os.WriteFile(filepath.Join(proofsDir, s.outputFile), []byte(formatted), 0644)
	}
}

func loadSignerPEM(t *testing.T, path string) principal.Signer {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read PEM: %v", err)
	}
	s, err := parseEd25519PEM(data)
	if err != nil {
		t.Fatalf("parse PEM: %v", err)
	}
	return s
}

func startSmeltStack(t *testing.T, tempDir, projectName string) {
	t.Helper()

	composePath := filepath.Join(tempDir, "compose.yml")
	exec.Command("docker", "network", "create", "storacha-network").CombinedOutput()

	t.Log("Starting smelt stack with docker compose (--pull never)...")
	cmd := exec.Command("docker", "compose",
		"-f", composePath, "-p", projectName,
		"up", "-d", "--pull", "never", "--wait", "--wait-timeout", "300",
	)
	cmd.Dir = tempDir
	cmd.Env = append(os.Environ(), "DOCKER_DEFAULT_PLATFORM=linux/amd64")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("Failed to start smelt stack: %v\n%s", err, string(out))
	}
	t.Log("Smelt stack started")

	t.Cleanup(func() {
		t.Log("Stopping smelt stack...")
		cmd := exec.Command("docker", "compose",
			"-f", composePath, "-p", projectName,
			"down", "-v", "--remove-orphans",
		)
		cmd.CombinedOutput()
	})
}

// ---------------------------------------------------------------------------
// DID discovery
// ---------------------------------------------------------------------------

func discoverServiceDIDExec(t *testing.T, projectName, service string) string {
	t.Helper()
	keyPath := "/keys/" + service + ".pem"
	cmd := exec.Command("docker", "compose", "-p", projectName, "exec", service, "cat", keyPath)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Logf("Could not read key from %s:%s: %v, falling back to did:web:%s", service, keyPath, err, service)
		return "did:web:" + service
	}
	didStr, err := didFromPEM(strings.TrimSpace(string(out)))
	if err != nil {
		t.Logf("Could not derive DID for %s: %v, falling back to did:web:%s", service, err, service)
		return "did:web:" + service
	}
	t.Logf("Discovered %s DID: %s", service, didStr)
	return didStr
}

func didFromPEM(pemData string) (string, error) {
	s, err := parseEd25519PEM([]byte(pemData))
	if err != nil {
		return "", err
	}
	return s.DID().String(), nil
}

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

// ---------------------------------------------------------------------------
// Credential setup
// ---------------------------------------------------------------------------

func setupCredentials(t *testing.T, ctx context.Context) *testEnv {
	t.Helper()
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
	t.Log("Logging in to local stack...")
	result, err := client.LoginAndSave(ctx, "test@test.example.com", nil)
	if err != nil {
		t.Fatalf("Login failed during setup: %v", err)
	}
	t.Logf("Login succeeded, got %d delegations", len(result.Delegations))
	spaces, err := client.Spaces()
	if err != nil {
		t.Fatalf("Failed to get spaces: %v", err)
	}
	if len(spaces) == 0 {
		t.Fatal("No spaces available after login")
	}
	t.Logf("Using space: %s", spaces[0].String())
	return &testEnv{signer: s, delegations: result.Delegations, spaceDID: spaces[0]}
}

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

// ---------------------------------------------------------------------------
// Test functions
// ---------------------------------------------------------------------------

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
	client := newClientFromEnv(t, env)
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
	client := newClientFromEnv(t, env)
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "list-test.txt")
	os.WriteFile(testFile, []byte("Upload list test content"), 0644)

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
		t.Errorf("Uploaded CID %s not found in upload list (%d items)", uploadResult.RootCID, len(listResult.Results))
	}
	t.Logf("UploadList succeeded: %d uploads, target found=%v", len(listResult.Results), found)
}

func testDownloadViaIndexer(t *testing.T, ctx context.Context, env *testEnv) {
	client := newClientFromEnv(t, env)
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
	client := newClientFromEnv(t, env)
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
