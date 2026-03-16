# Integration Tests for go-storacha-upload-client-kit

## Overview

Add integration tests that verify the client kit works end-to-end against a real Storacha network, using [smelt](https://github.com/storacha/smelt) to orchestrate a local stack via Docker Compose.

## Goals

- Verify upload, download, list, and login flows against a real service
- Run via build tag (`//go:build integration`) so they don't interfere with `go test ./...`
- Use smelt for local stack orchestration (upload service @ `:8080`, indexer @ `:9000`)
- No guppy container interaction — tests use the client kit's Go API directly from the host
- Tests must NOT use `t.Parallel()` (shared stack, sequential execution required)

## Architecture

### Stack Lifecycle

A single smelt stack is started once per test run. All tests run as subtests of a parent `TestIntegration` function. This ensures:
- The stack's `t.Cleanup()` is bound to the parent test's lifetime
- The stack is only torn down after all subtests complete
- Subtests can be selected individually with `-run TestIntegration/UploadFile`

```
TestIntegration(t *testing.T)
  ├── stack.MustNewStack(t)
  ├── Exec into upload container → cat /keys/upload.pem → derive DID
  ├── Exec into indexer container → cat key file → derive DID
  ├── Set env vars: STORACHA_SERVICE_URL, STORACHA_SERVICE_DID,
  │   STORACHA_INDEXING_SERVICE_URL, STORACHA_INDEXING_SERVICE_DID,
  │   STORACHA_RECEIPTS_URL
  ├── Create client, call Login() to get delegations + spaces
  ├── Cache: signer, delegations, spaceDID
  │
  ├── t.Run("Login", ...)
  ├── t.Run("UploadFile", ...)
  ├── t.Run("UploadDirectory", ...)
  ├── t.Run("UploadList", ...)
  ├── t.Run("DownloadViaIndexer", ...)
  ├── t.Run("DownloadViaGateway", ...)
  └── t.Run("LargeFileUpload", ...)
```

### Service Configuration

| Service | Host Port | Env Var (URL) | Env Var (DID) |
|---------|-----------|---------------|---------------|
| Upload (sprue) | 8080 | `STORACHA_SERVICE_URL=http://localhost:8080` | `STORACHA_SERVICE_DID` (see DID Discovery) |
| Indexer | 9000 | `STORACHA_INDEXING_SERVICE_URL=http://localhost:9000` | `STORACHA_INDEXING_SERVICE_DID` (see DID Discovery) |
| Receipts | 8080 | `STORACHA_RECEIPTS_URL=http://localhost:8080/receipt` | — |

**Note on hardcoded ports:** Smelt's compose files use static port mappings (`8080:80` for upload, `9000:80` for indexer). These are hardcoded. If those ports are in use on the host, Docker will fail with a bind error. This is documented as a known constraint.

### DID Discovery

Smelt generates Ed25519 keys on the host in a temp directory and mounts them into containers as read-only volumes. The `Stack.tempDir` field is unexported, so we discover DIDs by exec'ing into containers:

```go
// Read the PEM from inside the upload container
stdout, _, err := stack.Exec(ctx, "upload", "cat", "/keys/upload.pem")
```

The services are configured with `did:web:` identities (e.g., `did:web:upload`, `did:web:indexer`). We use these `did:web:` values for `STORACHA_SERVICE_DID` and `STORACHA_INDEXING_SERVICE_DID` — matching how the services identify themselves — rather than deriving `did:key:` from the raw Ed25519 public keys.

If `did:web:` values cannot be determined from the service config, we fall back to exec'ing into the container, parsing the PEM (PKCS#8 format), and deriving `did:key:` via go-ucanto's `ed25519.FromRaw()`.

### Credential Strategy

- **Login subtest**: Creates a fresh client with a new signer, calls `Login()` against the local delegator (which auto-approves in smelt's local environment). Asserts delegations and spaces are returned.
- **All other subtests**: Reuse credentials from a single `Login()` call performed during parent test setup. Each subtest creates its own `StorachaClient` using `NewStorachaClient(tmpDir)`, then sets the cached principal and loads the cached delegations.

**Login auto-approval:** Smelt's local delegator auto-approves authorization requests without email confirmation. The client kit's `Login()` calls `access/authorize` then polls `access/claim`. If auto-approval does not work as expected, the login will hang until `loginTimeout` (15 minutes). The parent test's timeout should catch this; if login fails, all subtests are skipped.

### Hazard: `log.Fatal` in `Must*` functions

Several internal functions (`MustGetConnection`, `MustGetIndexClient`, `MustGetReceiptsURL`) call `log.Fatal` on error, which calls `os.Exit(1)` and bypasses test cleanup. This means Docker containers could be left running if these functions fail. Mitigation: wrap client creation in the parent test with clear error handling; if the client cannot be created, call `t.Fatal()` (which runs cleanup) rather than letting `Must*` functions exit the process.

## Test Scenarios

### 1. Login
- Create client with `GenerateSigner()`
- Call `Login(ctx, "test@example.com", nil)`
- Assert: `LoginResult` has non-empty `AccountDID` and `Delegations`
- Assert: `client.Spaces()` returns at least one space

### 2. UploadFile
- Create client with cached credentials
- Write a small test file (e.g., "Hello, Storacha!")
- Call `UploadFile(ctx, spaceDID, filePath, &UploadOptions{Wrap: true})`
- Assert: `UploadResult.RootCID` is defined
- Assert: `UploadResult.URL` is non-empty

### 3. UploadDirectory
- Create temp directory with files and a subdirectory:
  ```
  testdir/
    file1.txt
    file2.txt
    subdir/file3.txt
  ```
- Call `UploadDirectory(ctx, spaceDID, dirPath, &UploadOptions{Wrap: true})`
- Assert: `UploadResult.RootCID` is defined

### 4. UploadList
- Upload a file, note the root CID
- Call `UploadList(ctx, spaceDID, uploadcap.ListCaveats{})` (zero-value struct, not nil)
- Assert: the uploaded CID appears in the returned list

### 5. DownloadViaIndexer
- Upload a file with known content
- Poll/retry for indexer propagation (up to 30 seconds)
- Call `DownloadFileViaIndexer(ctx, spaceDID, rootCID, outputPath)`
- Read the downloaded file
- Assert: content matches the original byte-for-byte

### 6. DownloadViaGateway
- Upload a file with known content
- Call `DownloadFile(ctx, rootCID, outputPath, &DownloadOptions{Gateway: "localhost:..."})` using whatever gateway the local stack exposes
- Read the downloaded file
- Assert: content matches the original

**Note:** Gateway download may not be available in smelt's local stack (it typically requires an IPFS gateway). If unavailable, skip with `t.Skip("gateway not available in local stack")`.

### 7. LargeFileUpload
- Generate a file larger than the UnixFS chunk size (>1 MiB, e.g., 5 MB) to produce multiple blocks
- Upload the file
- Download via indexer
- Assert: downloaded content matches original byte-for-byte

**Note on sharding:** The client kit currently creates a single CAR shard from all blocks — multi-CAR sharding is not implemented. This test verifies large file round-trip correctness, not multi-shard behavior. The shard count assertion is deferred until sharding is implemented.

## File Structure

```
integration_test.go       # Build-tagged //go:build integration
                          # Parent TestIntegration + all subtests + setup helpers
```

Single file using `NewStorachaClient` (guppy store format) for client creation. No new packages or directories.

## Dependencies

Add to `go.mod`:
- `github.com/storacha/smelt` (for `pkg/stack`)

## Running Tests

```bash
# Run integration tests (requires Docker)
go test -tags integration -v -timeout 15m

# Run a specific subtest
go test -tags integration -v -run TestIntegration/UploadFile -timeout 15m

# Run unit tests only (default, unchanged)
go test -v ./...
```

Timeout is 15 minutes to account for stack startup (~3 min) + test execution + indexer propagation delays.

## Error Handling

- Stack startup failure → `t.Fatal` in parent test (all subtests skipped)
- Login failure during setup → `t.Fatal` (all subtests skipped)
- Individual subtest failures are independent (shared stack, separate clients)
- Download tests include poll/retry for indexing propagation (up to 30s)
- `WithKeepOnFailure()` option on smelt stack for debugging failed tests

## Out of Scope

- `UploadCAR` — returns "not yet implemented" in the client kit
- Multi-CAR sharding assertions — not yet implemented in the client kit
- Directory download via indexer — `TestDownloadViaIndexer` covers file download only

## Risks & Mitigations

| Risk | Mitigation |
|------|------------|
| Smelt stack slow to start (~2-3 min) | Start once via parent test, share across subtests |
| Docker not available | Build tag ensures tests don't run by default |
| Port conflicts on 8080/9000 | Document requirement; fail fast with clear error |
| Indexer propagation delay | Poll/retry up to 30s before download assertions |
| Gateway not available locally | Skip gateway subtest if not available |
| DID discovery fails | Try `did:web:` first, fall back to exec + key derivation |
| `log.Fatal` in `Must*` bypasses cleanup | Wrap client creation with error handling in parent test |
| Login auto-approval may not work | Timeout at parent test level catches hanging login |
