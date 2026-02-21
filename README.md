# Go Storacha Upload Client Kit

A Go library for uploading files and directories to [Storacha](https://storacha.network/). This library provides a simple, high-level API for uploading content to decentralized storage.

## Features

- 📁 Upload single files or entire directories
- 🔄 Progress tracking for uploads
- 🎯 Direct upload to Storacha spaces
- 📦 Automatic UnixFS encoding
- 🔗 Returns IPFS CIDs and gateway URLs
- 🚀 Built on top of go-ucanto and go-libstoracha

## Installation

```bash
go get github.com/asabya/go-storacha-upload-client-kit
```

## Quick Start

### Prerequisites

1. You need a Storacha account and space
2. You use https://www.npmjs.com/package/@storacha/cli to login
3. Have delegated capabilities for `space/blob/add`, `space/index/add`, and `upload/add`

### Upload a Single File

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
    // Create a client with your agent store path
    client, err := kit.NewStorachaClient("/path/to/agent/store")
    if err != nil {
        log.Fatal(err)
    }

    // Parse your space DID
    spaceDID, err := did.Parse("did:key:z6Mkk...")
    if err != nil {
        log.Fatal(err)
    }

    // Upload a file
    ctx := context.Background()
    opts := &kit.UploadOptions{
        Wrap: true,
        OnProgress: func(uploaded int64) {
            fmt.Printf("Uploaded %d bytes\n", uploaded)
        },
    }

    result, err := kit.UploadFile(ctx, client, spaceDID, "/path/to/file.txt", opts)
    if err != nil {
        log.Fatal(err)
    }

    fmt.Printf("Upload successful!\n")
    fmt.Printf("CID: %s\n", result.RootCID.String())
    fmt.Printf("URL: %s\n", result.URL)
}
```

### Upload a Directory

```go
package main

import (
    "context"
    "fmt"
    "log"

    "github.com/asabya/go-storacha-upload-client-kit"
    "github.com/storacha/go-ucanto/did"
)

func main() {
    // Create a client
    client, err := go_storacha_upload_client_kit.NewStorachaClient("/path/to/agent/store")
    if err != nil {
        log.Fatal(err)
    }

    // Parse your space DID
    spaceDID, err := did.Parse("did:key:z6Mkk...")
    if err != nil {
        log.Fatal(err)
    }

    // Upload a directory
    ctx := context.Background()
    opts := &go_storacha_upload_client_kit.UploadOptions{
        Wrap: true,
    }

    result, err := go_storacha_upload_client_kit.UploadDirectory(ctx, client, spaceDID, "/path/to/directory", opts)
    if err != nil {
        log.Fatal(err)
    }

    fmt.Printf("Upload successful!\n")
    fmt.Printf("CID: %s\n", result.RootCID.String())
    fmt.Printf("URL: %s\n", result.URL)
}
```

## API Reference

### Client Creation

#### `NewStorachaClient(storePath string) (*StorachaClient, error)`

Creates a new Storacha client for uploading.

**Parameters:**
- `storePath`: Directory path where agent data will be stored

**Returns:**
- `*StorachaClient`: The client instance
- `error`: Any error that occurred

### Upload Functions

#### `UploadFile(ctx context.Context, client *StorachaClient, spaceDID did.DID, filePath string, opts *UploadOptions) (UploadResult, error)`

Uploads a single file to Storacha.

**Parameters:**
- `ctx`: Context for cancellation and timeouts
- `client`: Storacha client with necessary capabilities
- `spaceDID`: The space DID to upload to
- `filePath`: Path to the file to upload
- `opts`: Optional upload options

**Returns:**
- `UploadResult`: Contains the root CID and gateway URL
- `error`: Any error that occurred

#### `UploadDirectory(ctx context.Context, client *StorachaClient, spaceDID did.DID, dirPath string, opts *UploadOptions) (UploadResult, error)`

Uploads a directory of files to Storacha.

**Parameters:**
- `ctx`: Context for cancellation and timeouts
- `client`: Storacha client with necessary capabilities
- `spaceDID`: The space DID to upload to
- `dirPath`: Path to the directory to upload
- `opts`: Optional upload options

**Returns:**
- `UploadResult`: Contains the root CID and gateway URL
- `error`: Any error that occurred

### Types

#### `UploadOptions`

```go
type UploadOptions struct {
    // Dedupe enables deduplication (not currently implemented)
    Dedupe bool
    
    // Wrap wraps files in a directory (default: true)
    Wrap bool
    
    // OnProgress is called with upload progress updates
    OnProgress func(uploaded int64)
}
```

#### `UploadResult`

```go
type UploadResult struct {
    // RootCID is the content identifier for the uploaded data
    RootCID cid.Cid
    
    // URL is the IPFS gateway URL for the uploaded content
    URL string
}
```

### Client Methods

#### `client.DID() did.DID`

Returns the DID of the client.

#### `client.Spaces() ([]did.DID, error)`

Returns the list of spaces the client has access to.

### Utility Functions

#### `ParseSize(s string) (uint64, error)`

Parses a data size string with optional suffix (B, K, M, G).

**Examples:**
- `"1024"` → 1024 bytes
- `"512B"` → 512 bytes
- `"100K"` → 102400 bytes
- `"50M"` → 52428800 bytes
- `"2G"` → 2147483648 bytes

## Environment Variables

### `GUPPY_PRIVATE_KEY`

Your Storacha agent private key. This can be set to override the stored principal.

**Example:**
```bash
export GUPPY_PRIVATE_KEY="MgCYKXoHVy7Vk4/QjcEGi..."
```

### `STORACHA_SERVICE_URL`

The Storacha service URL (optional, defaults to `https://up.forge.storacha.network`).

### `STORACHA_SERVICE_DID`

The Storacha service DID (optional, defaults to `did:web:up.forge.storacha.network`).

### `STORACHA_RECEIPTS_URL`

The receipts service URL (optional, defaults to `https://up.forge.storacha.network/receipt`).

## Testing

Run the tests with:

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

The library extracts and reimplements the necessary client functionality from the guppy project to provide a standalone upload capability without requiring the full guppy client.

## Required Capabilities

To use this library, your agent must have the following delegated capabilities:

- `space/blob/add`: Permission to add blobs to a space
- `space/index/add`: Permission to add indexes to a space
- `upload/add`: Permission to register uploads
- `filecoin/offer`: Permission to offer content to Filecoin (optional, for larger files)

## Differences from Guppy CLI

This library is designed to be used as a standalone package and differs from the guppy CLI in several ways:

1. **No CLI Dependencies**: Doesn't depend on cobra or other CLI frameworks
2. **Simplified Client**: Uses a lightweight client wrapper instead of the full guppy Client
3. **Library-First Design**: Designed for programmatic use rather than command-line interaction
4. **Extracted Functionality**: Extracts only the upload-related code without the full preparation layer

## Contributing

Contributions are welcome! Please feel free to submit a Pull Request.

## License

[Add your license here]

## Acknowledgments

This library is built on top of:
- [go-ucanto](https://github.com/storacha/go-ucanto) - UCAN implementation in Go
- [go-libstoracha](https://github.com/storacha/go-libstoracha) - Storacha capabilities
- [guppy](https://github.com/storacha/guppy) - Storacha CLI (source of upload logic)

## Support

For issues and questions:
- GitHub Issues: [Create an issue](https://github.com/asabya/go-storacha-upload-client-kit/issues)
- Storacha Discord: [Join the community](https://discord.gg/storacha)
