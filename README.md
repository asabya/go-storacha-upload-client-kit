# Go Storacha Upload Client Kit

A Go library for uploading files and directories to [Storacha](https://storacha.network/). This library provides a simple, high-level API for uploading content to decentralized storage with full compatibility with the JavaScript storacha-cli.

## Features

- **Login Flow**: Complete email-based authentication compatible with JS storacha-cli
- Upload single files or entire directories
- Progress tracking for uploads
- Direct upload to Storacha spaces
- Automatic UnixFS encoding
- Returns IPFS CIDs and gateway URLs
- **JS-Compatible Store**: Reads/writes `storacha-cli.json` format compatible with JS CLI
- Built on top of go-ucanto and go-libstoracha

## ⚠️ Disclaimer

**This software is not tested and is provided "as is" without warranty of any kind.**

The authors and contributors are not responsible for any lost user data. Use this software at your own risk. Always do your own research (DYOR) and ensure you have proper backups before using this software with production data.

## Installation

```bash
go get github.com/asabya/go-storacha-upload-client-kit
```

## Quick Start

### Login and Upload

```go
package main

import (
    "context"
    "fmt"
    "log"

    kit "github.com/asabya/go-storacha-upload-client-kit"
    "github.com/storacha/go-ucanto/did"
)

func main() {
    ctx := context.Background()

    // Create a client - the store path is a directory where storacha-cli.json will be saved
    client, err := kit.NewStorachaClientFromW3Access("./my-store")
    if err != nil {
        log.Fatal(err)
    }

    // Check if principal (key) exists, generate if not
    if hasKey, _ := client.HasPrincipal(); !hasKey {
        signer, err := kit.GenerateSigner()
        if err != nil {
            log.Fatal(err)
        }
        if err := client.SetPrincipal(signer); err != nil {
            log.Fatal(err)
        }
    }

    // Login with email - this will wait for email confirmation
    result, err := client.LoginAndSave(ctx, "user@example.com", nil)
    if err != nil {
        log.Fatal(err)
    }
    fmt.Printf("Logged in as: %s\n", result.AccountDID)

    // Parse your space DID
    spaceDID, err := did.Parse("did:key:z6Mkk...")
    if err != nil {
        log.Fatal(err)
    }

    // Upload a file
    opts := &kit.UploadOptions{
        Wrap: true,
        OnProgress: func(uploaded int64) {
            fmt.Printf("Uploaded %d bytes\n", uploaded)
        },
    }

    uploadResult, err := client.UploadFile(ctx, spaceDID, "/path/to/file.txt", opts)
    if err != nil {
        log.Fatal(err)
    }

    fmt.Printf("Upload successful!\n")
    fmt.Printf("CID: %s\n", uploadResult.RootCID.String())
    fmt.Printf("URL: %s\n", uploadResult.URL)
}
```

### Use Existing JS CLI Store

If you've already logged in with the JavaScript storacha-cli, you can use that store directly:

```go
// On macOS, the JS CLI stores at: ~/Library/Preferences/w3access/
home, _ := os.UserHomeDir()
storePath := filepath.Join(home, "Library", "Preferences", "w3access")

client, err := kit.NewStorachaClientFromW3Access(storePath)
```

### Upload a Directory

```go
result, err := client.UploadDirectory(ctx, spaceDID, "/path/to/directory", &kit.UploadOptions{
    Wrap: true,
})
if err != nil {
    log.Fatal(err)
}
fmt.Printf("CID: %s\n", result.RootCID.String())
```

## Authentication

### Login Flow

The library implements the same login flow as the JavaScript storacha-cli:

1. **Generate or load a principal (signing key)**
2. **Request authorization** — sends an email with a confirmation link
3. **Poll for delegations** — waits for the user to click the link
4. **Save delegations** — stores the granted capabilities

```go
// LoginAndSave handles the complete flow
result, err := client.LoginAndSave(ctx, "user@example.com", &kit.LoginOptions{
    AppName: "my-app", // Optional: identifies your app in the email
})
```

### Store Format Compatibility

The library reads and writes the same `storacha-cli.json` format as the JavaScript CLI, ensuring interoperability:

- **Same file location**: `~/Library/Preferences/w3access/storacha-cli.json` (macOS)
- **Same JSON format**: Uses `$bytes` arrays and `$map` structures
- **Share credentials**: Login once, use with both Go and JS tools

### Client Types

| Constructor | Store Format | Use Case |
|-------------|-------------|----------|
| `NewStorachaClientFromW3Access(path)` | JS CLI compatible | Share with storacha-cli |
| `NewStorachaClient(path)` | Guppy native | Guppy ecosystem only |

## API Reference

### Client Creation

#### `NewStorachaClientFromW3Access(path string) (*StorachaClient, error)`

Creates a client that uses the JS CLI-compatible store format.

- `path`: Directory path where `storacha-cli.json` will be stored. Pass `""` to use the OS default location.

#### `NewStorachaClient(storePath string) (*StorachaClient, error)`

Creates a client using the native guppy store format.

### Authentication Methods

#### `client.Login(ctx, email, opts) (*LoginResult, error)`

Initiates the login flow. Blocks until email confirmation.

#### `client.LoginAndSave(ctx, email, opts) (*LoginResult, error)`

Login and automatically save delegations to the store. Recommended for most use cases.

#### `client.SaveDelegations(delegations...) error`

Persists delegations to the store.

#### `GenerateSigner() (principal.Signer, error)`

Package-level helper that generates a new Ed25519 signing key.

### Client Methods

#### `client.HasPrincipal() (bool, error)`

Returns true if a signing key is configured in the store.

#### `client.SetPrincipal(p principal.Signer) error`

Sets and persists the signing key for the client.

#### `client.DID() did.DID`

Returns the DID of the client's principal.

#### `client.Spaces() ([]did.DID, error)`

Returns all space DIDs the client has delegations for.

#### `client.AddProof(d delegation.Delegation)`

Adds an explicit delegation proof without persisting it to the store.

#### `client.AddProofFromFile(path string) error`

Loads a delegation proof from a CAR file.

### Upload Methods

#### `client.UploadFile(ctx, spaceDID, filePath, opts) (UploadResult, error)`

Uploads a single file to Storacha. When `opts.Wrap` is true (the default), the file is placed inside a directory node named after the original filename, matching the JS CLI behaviour.

**Required delegations:** `space/blob/add`, `space/index/add`, `upload/add`, `filecoin/offer`

#### `client.UploadDirectory(ctx, spaceDID, dirPath, opts) (UploadResult, error)`

Uploads a directory recursively, preserving the directory structure.

**Required delegations:** `space/blob/add`, `space/index/add`, `upload/add`, `filecoin/offer`

### Download Functions

These are package-level functions and do not require a client.

#### `DownloadFile(ctx, rootCID, outputPath, opts) error`

Downloads a file via an IPFS HTTP gateway (default: `ipfs.w3s.link`).

#### `DownloadDirectory(ctx, rootCID, outputDir, opts) error`

Downloads a directory via an IPFS HTTP gateway using CAR export.

#### `ReconstructFileFromCAR(carPath, rootCID, outputPath) error`

Reconstructs a file from a local CAR file. No network required.

#### `ReconstructDirectoryFromCAR(carPath, rootCID, outputDir) error`

Reconstructs a directory from a local CAR file. No network required.

### Indexer-Based Download Methods

Higher-reliability downloads that bypass the gateway by fetching blocks directly from their shard locations via the Storacha indexing service.

#### `client.DownloadFileViaIndexer(ctx, spaceDID, rootCID, outputPath) error`

#### `client.DownloadDirectoryViaIndexer(ctx, spaceDID, rootCID, outputDir) error`

### Types

#### `LoginOptions`

```go
type LoginOptions struct {
    AppName string // Optional app name shown in the confirmation email
}
```

#### `LoginResult`

```go
type LoginResult struct {
    AccountDID  did.DID                 // The account DID (did:mailto:...)
    Delegations []delegation.Delegation // Granted delegations
}
```

#### `UploadOptions`

```go
type UploadOptions struct {
    Dedupe     bool             // Enable deduplication (not yet implemented)
    Wrap       bool             // Wrap files in a directory (default: true)
    OnProgress func(int64)     // Progress callback; argument is bytes uploaded so far
}
```

#### `UploadResult`

```go
type UploadResult struct {
    RootCID cid.Cid // Content identifier for the uploaded data
    URL     string  // IPFS gateway URL
}
```

#### `DownloadOptions`

```go
type DownloadOptions struct {
    OnProgress func(int64) // Progress callback; argument is bytes downloaded so far
    Gateway    string      // Override the default IPFS gateway (ipfs.w3s.link)
}
```

### Utility Functions

#### `ParseSize(s string) (uint64, error)`

Parses a data size string with optional suffix (B, K, M, G).

```
"1024"  → 1024 bytes
"512B"  → 512 bytes
"100K"  → 102400 bytes
"50M"   → 52428800 bytes
```

#### `DefaultW3AccessStorePath() string`

Returns the OS-appropriate default path for the JS CLI store.

## Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `GUPPY_PRIVATE_KEY` | — | Agent private key; overrides the stored principal |
| `STORACHA_SERVICE_URL` | `https://up.storacha.network` | Storacha service URL |
| `STORACHA_SERVICE_DID` | `did:web:up.storacha.network` | Storacha service DID |
| `STORACHA_RECEIPTS_URL` | `https://up.storacha.network/receipt` | Receipts service URL |
| `STORACHA_INDEXING_SERVICE_URL` | `https://indexer.storacha.network` | Indexing service URL |
| `STORACHA_INDEXING_SERVICE_DID` | `did:key:z6Mkq...` | Indexing service DID |

## Testing

```bash
go test -v ./...
```

Some tests require valid Storacha credentials and will be skipped if `GUPPY_PRIVATE_KEY` is not set.

## Architecture

This library is built on top of several key components:

1. **UnixFS Encoding** — files and directories are encoded using the UnixFS format
2. **CAR Files** — content is packaged into CAR (Content Addressable aRchive) files
3. **Blob Upload** — CAR files are uploaded as blobs to Storacha
4. **Index Creation** — sharded DAG indexes are created for efficient retrieval
5. **Upload Registration** — uploads are registered with the service

## Required Capabilities

To upload, your agent must have the following delegated capabilities for the target space:

- `space/blob/add` — add blobs to a space
- `space/index/add` — add indexes to a space
- `upload/add` — register uploads
- `filecoin/offer` — offer content to Filecoin (optional)

## License

MIT

## Acknowledgments

- [go-ucanto](https://github.com/storacha/go-ucanto) — UCAN implementation in Go
- [go-libstoracha](https://github.com/storacha/go-libstoracha) — Storacha capabilities
- [guppy](https://github.com/storacha/guppy) — Storacha CLI (source of upload logic)

## Support

For issues and questions, open a [GitHub issue](https://github.com/asabya/go-storacha-upload-client-kit/issues).
