package main

import (
	"context"
	"fmt"
	"log"
	"os"

	kit "github.com/asabya/go-storacha-upload-client-kit"
	"github.com/ipfs/go-cid"
	uploadcap "github.com/storacha/go-libstoracha/capabilities/upload"
	"github.com/storacha/go-ucanto/did"
	ed25519signer "github.com/storacha/go-ucanto/principal/ed25519/signer"
)

// newClient creates a StorachaClient using the JS CLI format.
func newClient(storePath string) (*kit.StorachaClient, error) {
	return kit.NewStorachaClientFromW3Access(storePath)
}

// newClientWithNewKey creates a client with a new key if one doesn't exist.
func newClientWithNewKey(storePath string) (*kit.StorachaClient, error) {
	client, err := kit.NewStorachaClientFromW3Access(storePath)
	if err != nil {
		return nil, err
	}

	// Check if principal exists
	hasPrincipal, err := client.HasPrincipal()
	if err != nil {
		return nil, fmt.Errorf("checking principal: %w", err)
	}

	if !hasPrincipal {
		// Generate new key
		signer, err := ed25519signer.Generate()
		if err != nil {
			return nil, fmt.Errorf("generating key: %w", err)
		}
		if err := client.SetPrincipal(signer); err != nil {
			return nil, fmt.Errorf("setting principal: %w", err)
		}
		fmt.Printf("Generated new agent key: %s\n", signer.DID().String())
	}

	return client, nil
}

func main() {
	if len(os.Args) < 2 {
		printUsage()
	}

	command := os.Args[1]

	switch command {
	case "login":
		handleLogin()
	case "upload":
		handleUpload()
	case "download":
		handleDownload()
	case "reconstruct":
		handleReconstruct()
	case "list":
		handleList()
	default:
		log.Fatalf("Unknown command: %s", command)
	}
}

func printUsage() {
	fmt.Println("Usage:")
	fmt.Println("  Login:       example login <agent-store-path> <email>")
	fmt.Println("  Upload:      example upload <agent-store-path> <space-did> <file-or-dir-path> [proof.car ...]")
	fmt.Println("  Download:    example download <root-cid> <output-path> [store-path] [space-did]")
	fmt.Println("  Reconstruct: example reconstruct <car-file> <root-cid> <output-path>")
	fmt.Println("  List:        example list <agent-store-path> <space-did>")
	fmt.Println()
	fmt.Println("Store paths:")
	fmt.Println("  ~/Library/Preferences/w3access/storacha-cli.json   JS CLI store (macOS)")
	fmt.Println("  ~/.config/w3access/storacha-cli.json                JS CLI store (Linux)")
	fmt.Println("  ~/.storacha/guppy                                   guppy agent-store directory")
	fmt.Println()
	fmt.Println("Examples:")
	fmt.Println("  example login ~/Library/Preferences/w3access/storacha-cli.json user@example.com")
	fmt.Println("  example upload ~/Library/Preferences/w3access/storacha-cli.json did:key:z6Mkk... ./myfile.txt")
	fmt.Println("  example download bafybeib... ./downloaded.txt")
	fmt.Println("  example download bafybeib... ./downloaded.txt ~/Library/Preferences/w3access/storacha-cli.json did:key:z6Mkk...")
	fmt.Println("  example reconstruct ./data.car bafybeib... ./reconstructed.txt")
	fmt.Println("  example list ~/Library/Preferences/w3access/storacha-cli.json did:key:z6Mkk...")
	os.Exit(1)
}

func handleLogin() {
	if len(os.Args) < 4 {
		log.Fatal("Usage: example login <agent-store-path> <email>")
	}

	storePath := os.Args[2]
	email := os.Args[3]

	fmt.Println("Creating Storacha client...")
	client, err := newClientWithNewKey(storePath)
	if err != nil {
		log.Fatalf("Failed to create client: %v", err)
	}

	clientDID := client.DID()
	fmt.Printf("Client DID: %s\n", clientDID.String())

	fmt.Printf("Logging in as: %s\n", email)
	fmt.Println("Check your email for a verification link...")

	ctx := context.Background()
	result, err := client.LoginAndSave(ctx, email, nil)
	if err != nil {
		log.Fatalf("Login failed: %v", err)
	}

	fmt.Println()
	fmt.Println("✅ Login successful!")
	fmt.Printf("Account DID: %s\n", result.AccountDID.String())
	fmt.Printf("Received %d delegation(s)\n", len(result.Delegations))
	fmt.Printf("Delegations saved to: %s\n", storePath)
}

func handleUpload() {
	if len(os.Args) < 5 {
		log.Fatal("Usage: example upload <agent-store-path> <space-did> <file-or-dir-path> [proof.car ...]")
	}

	storePath := os.Args[2]
	spaceDIDStr := os.Args[3]
	uploadPath := os.Args[4]
	// Any extra arguments are proof CAR files (needed when the store has no
	// built-in delegations, e.g. a ~/.storacha store set up by the JS CLI).
	proofPaths := os.Args[5:]

	// Parse space DID
	spaceDID, err := did.Parse(spaceDIDStr)
	if err != nil {
		log.Fatalf("Invalid space DID: %v", err)
	}

	// Create client
	fmt.Println("Creating Storacha client...")
	client, err := newClient(storePath)
	if err != nil {
		log.Fatalf("Failed to create client: %v", err)
	}

	// Load any explicitly provided proof files.
	// This is required when the store has no built-in delegations
	// (e.g. ~/.storacha set up by the JS upload-service CLI).
	for _, proofPath := range proofPaths {
		if err := client.AddProofFromFile(proofPath); err != nil {
			log.Fatalf("Failed to load proof %s: %v", proofPath, err)
		}
		fmt.Printf("Loaded proof: %s\n", proofPath)
	}

	// Check client DID
	clientDID := client.DID()
	if clientDID.String() == "" {
		log.Fatal("No DID found - please ensure your agent store is properly configured")
	}
	fmt.Printf("Client DID: %s\n", clientDID.String())

	// Check if path is a file or directory
	info, err := os.Stat(uploadPath)
	if err != nil {
		log.Fatalf("Failed to stat path: %v", err)
	}

	ctx := context.Background()
	opts := &kit.UploadOptions{
		Wrap: true,
		OnProgress: func(uploaded int64) {
			fmt.Printf("\rUploaded: %d bytes", uploaded)
		},
	}

	var result kit.UploadResult

	if info.IsDir() {
		fmt.Printf("Uploading directory: %s\n", uploadPath)
		result, err = client.UploadDirectory(ctx, spaceDID, uploadPath, opts)
	} else {
		fmt.Printf("Uploading file: %s\n", uploadPath)
		result, err = client.UploadFile(ctx, spaceDID, uploadPath, opts)
	}

	if err != nil {
		log.Fatalf("Upload failed: %v", err)
	}

	fmt.Println() // New line after progress
	fmt.Println("✅ Upload successful!")
	fmt.Printf("Root CID: %s\n", result.RootCID.String())
	fmt.Printf("Access at: %s\n", result.URL)
}

func handleDownload() {
	if len(os.Args) < 4 {
		log.Fatal("Usage: example download <root-cid> <output-path> [store-path] [space-did]")
	}

	rootCIDStr := os.Args[2]
	outputPath := os.Args[3]

	// Parse root CID
	rootCID, err := cid.Decode(rootCIDStr)
	if err != nil {
		log.Fatalf("Invalid root CID: %v", err)
	}

	// If store path and space DID are provided, use indexer-based download
	if len(os.Args) >= 6 {
		store := os.Args[4]
		spaceDIDStr := os.Args[5]

		client, err := newClient(store)
		if err != nil {
			log.Fatalf("Failed to create client: %v", err)
		}
		spaceDID, err := did.Parse(spaceDIDStr)
		if err != nil {
			log.Fatalf("Failed to parse space DID: %v", err)
		}

		fmt.Printf("Downloading CID via indexer: %s\n", rootCID.String())
		err = client.DownloadFileViaIndexer(context.Background(), spaceDID, rootCID, outputPath)
		if err != nil {
			log.Println("Error downloading file via indexer:", err)
			fmt.Println("Trying as directory via indexer...")
			err = client.DownloadDirectoryViaIndexer(context.Background(), spaceDID, rootCID, outputPath)
			if err != nil {
				log.Fatalf("Download via indexer failed: %v", err)
			}
		}

		fmt.Println()
		fmt.Println("✅ Download successful!")
		fmt.Printf("Saved to: %s\n", outputPath)
		return
	}

	ctx := context.Background()
	opts := &kit.DownloadOptions{
		OnProgress: func(downloaded int64) {
			fmt.Printf("\rDownloaded: %d bytes", downloaded)
		},
	}

	fmt.Printf("Downloading CID: %s\n", rootCID.String())

	// Try to download as file first
	err = kit.DownloadFile(ctx, rootCID, outputPath, opts)
	if err != nil {
		log.Println("Error downloading file:", err)
		// If file download fails, try as directory
		fmt.Printf("\nTrying to download as directory...\n")
		err = kit.DownloadDirectory(ctx, rootCID, outputPath, opts)
		if err != nil {
			log.Fatalf("Download failed: %v", err)
		}
	}

	fmt.Println() // New line after progress
	fmt.Println("✅ Download successful!")
	fmt.Printf("Saved to: %s\n", outputPath)
}

func handleReconstruct() {
	if len(os.Args) < 5 {
		log.Fatal("Usage: example reconstruct <car-file> <root-cid> <output-path>")
	}

	carFile := os.Args[2]
	rootCIDStr := os.Args[3]
	outputPath := os.Args[4]

	// Parse root CID
	rootCID, err := cid.Decode(rootCIDStr)
	if err != nil {
		log.Fatalf("Invalid root CID: %v", err)
	}

	// Check if CAR file exists
	if _, err := os.Stat(carFile); err != nil {
		log.Fatalf("CAR file not found: %v", err)
	}

	fmt.Printf("Reconstructing from CAR: %s\n", carFile)
	fmt.Printf("Root CID: %s\n", rootCID.String())

	// First, parse the CAR header to get info
	carData, err := os.ReadFile(carFile)
	if err != nil {
		log.Fatalf("Failed to read CAR file: %v", err)
	}

	roots, blockCount, err := kit.ParseCARHeader(carData)
	if err != nil {
		log.Fatalf("Failed to parse CAR header: %v", err)
	}

	fmt.Printf("CAR contains %d blocks\n", blockCount)
	if len(roots) > 0 {
		fmt.Printf("CAR roots: %v\n", roots)
	}

	// Extract blocks to determine if it's a file or directory
	blocks, _, err := kit.ExtractCARBlocks(carData)
	if err != nil {
		log.Fatalf("Failed to extract blocks: %v", err)
	}

	// Get the root block and determine its type
	nodeType, size, err := kit.GetUnixFSNodeInfo(blocks, rootCID)
	if err != nil {
		log.Fatalf("Failed to determine node type: %v", err)
	}

	fmt.Printf("Node type: %s (size/entries: %d)\n", nodeType, size)

	// Reconstruct based on type
	if nodeType == "directory" {
		fmt.Println("Reconstructing directory...")
		err = kit.ReconstructDirectoryFromCAR(carFile, rootCID, outputPath)
	} else {
		fmt.Println("Reconstructing file...")
		err = kit.ReconstructFileFromCAR(carFile, rootCID, outputPath)
	}

	if err != nil {
		log.Fatalf("Reconstruction failed: %v", err)
	}

	fmt.Println("✅ Reconstruction successful!")
	fmt.Printf("Saved to: %s\n", outputPath)
}

func handleList() {
	if len(os.Args) < 4 {
		log.Fatal("Usage: example list <agent-store-path> <space-did>")
	}

	storePath := os.Args[2]
	spaceDIDStr := os.Args[3]

	spaceDID, err := did.Parse(spaceDIDStr)
	if err != nil {
		log.Fatalf("Invalid space DID: %v", err)
	}

	client, err := newClient(storePath)
	if err != nil {
		log.Fatalf("Failed to create client: %v", err)
	}

	ctx := context.Background()
	var cursor *string
	total := 0
	for {
		listOk, err := client.UploadList(ctx, spaceDID, uploadcap.ListCaveats{Cursor: cursor})
		if err != nil {
			log.Fatalf("UploadList failed: %v", err)
		}

		for _, r := range listOk.Results {
			fmt.Println(r.Root)
			total++
		}

		if listOk.Cursor == nil {
			break
		}
		cursor = listOk.Cursor
	}

	fmt.Printf("\nTotal uploads: %d\n", total)
}
