# Go Storacha Upload Client Kit

A Go library for uploading files and directories to [Storacha](https://storacha.network/). This library provides a simple, high-level API for uploading content to decentralized storage with full compatibility with the JavaScript storacha-cli.

## Features

- 🔐 **Login Flow**: Complete email-based authentication compatible with JS storacha-cli
- 📁 Upload single files or entire directories
- 🔄 Progress tracking for uploads
- 🎯 Direct upload to Storacha spaces
- 📦 Automatic UnixFS encoding
- 🔗 Returns IPFS CIDs and gateway URLs
- 💾 **JS-Compatible Store**: Reads/writes `storacha-cli.json` format compatible with JS CLI
- 🚀 Built on top of go-ucanto and go-libstoracha

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
    result, err := kit.LoginAndSave(ctx, client, "user@example.com", nil)
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

    uploadResult, err := kit.UploadFile(ctx, client, spaceDID, "/path/to/file.txt", opts)
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
result, err := kit.UploadDirectory(ctx, client, spaceDID, "/path/to/directory", &kit.UploadOptions{
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
2. **Request authorization** - sends an email with a confirmation link
3. **Poll for delegations** - waits for the user to click the link
4. **Save delegations** - stores the granted capabilities

```go
// LoginAndSave handles the complete flow
result, err := kit.LoginAndSave(ctx, client, "user@example.com", &kit.LoginOptions{
    AppName: "my-app", // Optional: identifies your app in the email
})
```

### Store Format Compatibility

The library reads and writes the same `storacha-cli.json` format as the JavaScript CLI, ensuring interoperability:

- **Same file location**: `~/Library/Preferences/w3access/storacha-cli.json` (macOS)
- **Same JSON format**: Uses `$bytes` arrays and `$map` structures
- **Share credentials**: Login once, use with both Go and JS tools

### Client Types

| Function | Store Format | Use Case |
|----------|-------------|----------|
| `NewStorachaClientFromW3Access(path)` | JS CLI compatible | Share with storacha-cli |
| `NewStorachaClient(path)` | Guppy native | Guppy ecosystem only |

## API Reference

### Client Creation

#### `NewStorachaClientFromW3Access(path string) (*StorachaClient, error)`

Creates a client that uses the JS CLI-compatible store format.

**Parameters:**
- `path`: Directory path where `storacha-cli.json` will be stored

**Returns:**
- `*StorachaClient`: The client instance
- `error`: Any error that occurred

#### `NewStorachaClient(storePath string) (*StorachaClient, error)`

Creates a client using the native guppy store format.

### Authentication Functions

#### `Login(ctx context.Context, client *StorachaClient, email string, opts *LoginOptions) (*LoginResult, error)`

Initiates the login flow. Returns after email confirmation.

#### `LoginAndSave(ctx context.Context, client *StorachaClient, email string, opts *LoginOptions) (*LoginResult, error)`

Login and automatically save delegations to the store.

#### `GenerateSigner() (principal.Signer, error)`

Generates a new Ed25519 signing key for the client.

### Client Methods

#### `client.HasPrincipal() (bool, error)`

Returns true if a principal (signing key) is configured.

#### `client.SetPrincipal(p principal.Signer) error`

Sets the principal for the client.

#### `client.DID() did.DID`

Returns the DID of the client.

#### `client.Spaces() ([]did.DID, error)`

Returns the list of spaces the client has access to.

### Upload Functions

#### `UploadFile(ctx context.Context, client *StorachaClient, spaceDID did.DID, filePath string, opts *UploadOptions) (UploadResult, error)`

Uploads a single file to Storacha.

#### `UploadDirectory(ctx context.Context, client *StorachaClient, spaceDID did.DID, dirPath string, opts *UploadOptions) (UploadResult, error)`

Uploads a directory of files to Storacha.

### Types

#### `LoginOptions`

```go
type LoginOptions struct {
    AppName string  // Optional app name shown in confirmation email
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
    Dedupe     bool              // Enable deduplication (not implemented)
    Wrap       bool              // Wrap files in a directory (default: true)
    OnProgress func(uploaded int64)  // Progress callback
}
```

#### `UploadResult`

```go
type UploadResult struct {
    RootCID cid.Cid  // Content identifier for uploaded data
    URL     string   // IPFS gateway URL
}
```

### Utility Functions

#### `ParseSize(s string) (uint64, error)`

Parses a data size string with optional suffix (B, K, M, G).

**Examples:**
- `"1024"` → 1024 bytes
- `"512B"` → 512 bytes
- `"100K"` → 102400 bytes
- `"50M"` → 52428800 bytes

#### `DefaultW3AccessStorePath() string`

Returns the OS-appropriate default path for the JS CLI store.

## Environment Variables

### `GUPPY_PRIVATE_KEY`

Your Storacha agent private key. Overrides the stored principal.

```bash
export GUPPY_PRIVATE_KEY="MgCYKXoHVy7Vk4/QjcEGi..."
```

### `STORACHA_SERVICE_URL`

The Storacha service URL (default: `https://up.forge.storacha.network`).

### `STORACHA_SERVICE_DID`

The Storacha service DID (default: `did:web:up.forge.storacha.network`).

### `STORACHA_RECEIPTS_URL`

The receipts service URL (default: `https://up.forge.storacha.network/receipt`).

## Testing

```bash
go test -v
```

Note: Some tests require valid Storacha credentials and will be skipped if `GUPPY_PRIVATE_KEY` is not set.

## Architecture

This library is built on top of several key components:

1. **UnixFS Encoding**: Files and directories are encoded using the UnixFS format
2. **CAR Files**: Content is packaged into CAR (Content Addressable aRchive) files
3. **Blob Upload**: CAR files are uploaded as blobs to Storacha
4. **Index Creation**: Sharded DAG indexes are created for efficient retrieval
5. **Upload Registration**: Uploads are registered with the service

## Required Capabilities

To use this library, your agent must have the following delegated capabilities:

- `space/blob/add`: Permission to add blobs to a space
- `space/index/add`: Permission to add indexes to a space
- `upload/add`: Permission to register uploads
- `filecoin/offer`: Permission to offer content to Filecoin (optional)

## License

MIT

## Acknowledgments

This library is built on top of:
- [go-ucanto](https://github.com/storacha/go-ucanto) - UCAN implementation in Go
- [go-libstoracha](https://github.com/storacha/go-libstoracha) - Storacha capabilities
- [guppy](https://github.com/storacha/guppy) - Storacha CLI (source of upload logic)

## Support

For issues and questions:
- GitHub Issues: [Create an issue](https://github.com/asabya/go-storacha-upload-client-kit/issues)