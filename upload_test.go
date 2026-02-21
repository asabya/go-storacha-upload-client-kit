package go_storacha_upload_client_kit

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/storacha/go-ucanto/did"
)

// TestUploadFile tests uploading a single file to Storacha
func TestUploadFile(t *testing.T) {
	// Skip if no credentials are available
	if os.Getenv("GUPPY_PRIVATE_KEY") == "" {
		t.Skip("Skipping test: GUPPY_PRIVATE_KEY not set")
	}

	// Create a temporary directory for the agent store
	tmpDir, err := os.MkdirTemp("", "storacha-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create a test file
	testFile := filepath.Join(tmpDir, "test.txt")
	testContent := []byte("Hello, Storacha!")
	if err := os.WriteFile(testFile, testContent, 0644); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	// Create client
	client, err := NewStorachaClient(tmpDir)
	if err != nil {
		t.Fatalf("Failed to create client: %v", err)
	}

	// Get available spaces
	spaces, err := client.Spaces()
	if err != nil {
		t.Fatalf("Failed to get spaces: %v", err)
	}

	if len(spaces) == 0 {
		t.Skip("No spaces available for testing")
	}

	spaceDID := spaces[0]
	t.Logf("Using space: %s", spaceDID.String())

	// Test upload
	ctx := context.Background()
	opts := &UploadOptions{
		Wrap: true,
		OnProgress: func(uploaded int64) {
			t.Logf("Progress: %d bytes uploaded", uploaded)
		},
	}

	result, err := client.UploadFile(ctx, spaceDID, testFile, opts)
	if err != nil {
		t.Fatalf("Failed to upload file: %v", err)
	}

	t.Logf("Upload successful!")
	t.Logf("Root CID: %s", result.RootCID.String())
	t.Logf("URL: %s", result.URL)

	// Verify the CID is valid
	if !result.RootCID.Defined() {
		t.Error("Expected a defined CID")
	}
}

// TestUploadDirectory tests uploading a directory to Storacha
func TestUploadDirectory(t *testing.T) {
	// Skip if no credentials are available
	if os.Getenv("GUPPY_PRIVATE_KEY") == "" {
		t.Skip("Skipping test: GUPPY_PRIVATE_KEY not set")
	}

	// Create a temporary directory for the agent store
	tmpDir, err := os.MkdirTemp("", "storacha-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create a test directory with multiple files
	testDir := filepath.Join(tmpDir, "testdir")
	if err := os.MkdirAll(testDir, 0755); err != nil {
		t.Fatalf("Failed to create test directory: %v", err)
	}

	// Create test files
	files := map[string]string{
		"file1.txt":        "Content of file 1",
		"file2.txt":        "Content of file 2",
		"subdir/file3.txt": "Content of file 3 in subdirectory",
	}

	for path, content := range files {
		fullPath := filepath.Join(testDir, path)
		dir := filepath.Dir(fullPath)
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatalf("Failed to create directory %s: %v", dir, err)
		}
		if err := os.WriteFile(fullPath, []byte(content), 0644); err != nil {
			t.Fatalf("Failed to create test file %s: %v", path, err)
		}
	}

	// Create client
	client, err := NewStorachaClient(tmpDir)
	if err != nil {
		t.Fatalf("Failed to create client: %v", err)
	}

	// Get available spaces
	spaces, err := client.Spaces()
	if err != nil {
		t.Fatalf("Failed to get spaces: %v", err)
	}

	if len(spaces) == 0 {
		t.Skip("No spaces available for testing")
	}

	spaceDID := spaces[0]
	t.Logf("Using space: %s", spaceDID.String())

	// Test upload
	ctx := context.Background()
	opts := &UploadOptions{
		Wrap: true,
		OnProgress: func(uploaded int64) {
			t.Logf("Progress: %d bytes uploaded", uploaded)
		},
	}

	result, err := client.UploadDirectory(ctx, spaceDID, testDir, opts)
	if err != nil {
		t.Fatalf("Failed to upload directory: %v", err)
	}

	t.Logf("Upload successful!")
	t.Logf("Root CID: %s", result.RootCID.String())
	t.Logf("URL: %s", result.URL)

	// Verify the CID is valid
	if !result.RootCID.Defined() {
		t.Error("Expected a defined CID")
	}
}

// TestClientCreation tests creating a Storacha client
func TestClientCreation(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "storacha-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	client, err := NewStorachaClient(tmpDir)
	if err != nil {
		t.Fatalf("Failed to create client: %v", err)
	}

	if client == nil {
		t.Error("Expected non-nil client")
	}

	// Test DID method (may be empty if no principal is set)
	clientDID := client.DID()
	t.Logf("Client DID: %s", clientDID.String())
}

// TestParseSizeUtil tests the ParseSize utility function
func TestParseSizeUtil(t *testing.T) {
	tests := []struct {
		input    string
		expected uint64
		wantErr  bool
	}{
		{"1024", 1024, false},
		{"512B", 512, false},
		{"100K", 102400, false},
		{"50M", 52428800, false},
		{"2G", 2147483648, false},
		{"", 0, true},
		{"invalid", 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result, err := ParseSize(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseSize(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
				return
			}
			if result != tt.expected {
				t.Errorf("ParseSize(%q) = %d, want %d", tt.input, result, tt.expected)
			}
		})
	}
}

// ExampleStorachaClient_UploadFile demonstrates how to upload a single file
func ExampleStorachaClient_UploadFile() {
	// Create a client with your agent store path
	client, err := NewStorachaClient("/path/to/agent/store")
	if err != nil {
		panic(err)
	}

	// Parse your space DID
	spaceDID, err := did.Parse("did:key:z6Mkk...")
	if err != nil {
		panic(err)
	}

	// Upload a file
	ctx := context.Background()
	opts := &UploadOptions{
		Wrap: true,
		OnProgress: func(uploaded int64) {
			fmt.Printf("Uploaded %d bytes\n", uploaded)
		},
	}

	result, err := client.UploadFile(ctx, spaceDID, "/path/to/file.txt", opts)
	if err != nil {
		panic(err)
	}

	fmt.Printf("Upload successful! CID: %s\n", result.RootCID.String())
	fmt.Printf("Access at: %s\n", result.URL)
}

// ExampleStorachaClient_UploadDirectory demonstrates how to upload a directory
func ExampleStorachaClient_UploadDirectory() {
	// Create a client with your agent store path
	client, err := NewStorachaClient("/path/to/agent/store")
	if err != nil {
		panic(err)
	}

	// Parse your space DID
	spaceDID, err := did.Parse("did:key:z6Mkk...")
	if err != nil {
		panic(err)
	}

	// Upload a directory
	ctx := context.Background()
	opts := &UploadOptions{
		Wrap: true,
	}

	result, err := client.UploadDirectory(ctx, spaceDID, "/path/to/directory", opts)
	if err != nil {
		panic(err)
	}

	fmt.Printf("Upload successful! CID: %s\n", result.RootCID.String())
	fmt.Printf("Access at: %s\n", result.URL)
}
