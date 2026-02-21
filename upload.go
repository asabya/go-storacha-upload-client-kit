// Package go_storacha_upload_client_kit provides a library for uploading files and directories to Storacha.
package go_storacha_upload_client_kit

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"

	commcid "github.com/filecoin-project/go-fil-commcid"
	"github.com/ipfs/go-cid"
	cbor "github.com/ipfs/go-ipld-cbor"
	"github.com/ipfs/go-unixfsnode/data/builder"
	ipldcar "github.com/ipld/go-car"
	"github.com/ipld/go-car/util"
	dagpb "github.com/ipld/go-codec-dagpb"
	"github.com/ipld/go-ipld-prime"
	"github.com/ipld/go-ipld-prime/codec"
	"github.com/ipld/go-ipld-prime/datamodel"
	"github.com/ipld/go-ipld-prime/linking"
	cidlink "github.com/ipld/go-ipld-prime/linking/cid"
	"github.com/multiformats/go-multihash"
	"github.com/multiformats/go-varint"
	commp "github.com/storacha/go-fil-commp-hashhash"
	"github.com/storacha/go-libstoracha/blobindex"
	"github.com/storacha/go-ucanto/did"
)

const (
	// MinPiecePayload is the minimum payload size for a piece CID to be computed.
	// Blobs smaller than this will not have a piece CID computed.
	MinPiecePayload = 127
)

// UploadOptions contains options for upload functions
type UploadOptions struct {
	// Dedupe enables deduplication (not currently implemented)
	Dedupe bool
	// Wrap wraps files in a directory (default: true, set to false for single file uploads)
	Wrap bool
	// OnProgress is called with upload progress updates
	OnProgress func(uploaded int64)
}

// UploadResult contains the result of an upload operation
type UploadResult struct {
	// RootCID is the content identifier for the uploaded data
	RootCID cid.Cid
	// URL is the IPFS gateway URL for the uploaded content
	URL string
}

// Block represents an IPLD block with CID and data
type Block struct {
	CID  cid.Cid
	Data []byte
}

// shardMetadata contains information about a stored shard
type shardMetadata struct {
	cid   cid.Cid
	size  uint64
	piece cid.Cid
}

// UploadFile uploads a single file to the service and returns the root data CID.
// This mimics the JavaScript uploadFile function by directly calling client methods.
//
// Required delegated capability proofs: `space/blob/add`, `space/index/add`, `upload/add`, `filecoin/offer`
//
// Parameters:
//   - ctx: Context for cancellation and timeouts
//   - client: Storacha client with necessary capabilities
//   - spaceDID: The space DID to upload to
//   - filePath: Path to the file to upload
//   - opts: Optional upload options
//
// Returns the root CID of the uploaded file.
func UploadFile(ctx context.Context, client *StorachaClient, spaceDID did.DID, filePath string, opts *UploadOptions) (UploadResult, error) {
	if opts == nil {
		opts = &UploadOptions{Wrap: true}
	}

	// Open the file
	file, err := os.Open(filePath)
	if err != nil {
		return UploadResult{}, fmt.Errorf("opening file: %w", err)
	}
	defer file.Close()

	var blocks []Block
	ls := createBlockCapturingLinkSystem(&blocks)

	// Encode file as UnixFS blocks
	fileLink, fileSize, err := builder.BuildUnixFSFile(file, "size-1048576", ls)
	if err != nil {
		return UploadResult{}, fmt.Errorf("building UnixFS file: %w", err)
	}

	rootCID := fileLink.(cidlink.Link).Cid

	// When Wrap is true, place the file inside a directory node named after the
	// original filename — matching the behaviour of the storacha JS CLI uploadFile.
	if opts.Wrap {
		fileName := filepath.Base(filePath)
		dirEntry, err := builder.BuildUnixFSDirectoryEntry(fileName, int64(fileSize), fileLink.(cidlink.Link))
		if err != nil {
			return UploadResult{}, fmt.Errorf("building directory entry: %w", err)
		}
		dirLink, _, err := builder.BuildUnixFSDirectory([]dagpb.PBLink{dirEntry}, ls)
		if err != nil {
			return UploadResult{}, fmt.Errorf("wrapping file in directory: %w", err)
		}
		rootCID = dirLink.(cidlink.Link).Cid
	}

	// Upload the blocks (creates CAR, uploads blob, registers index and upload)
	if err := uploadBlocks(ctx, client, spaceDID, blocks, rootCID, opts); err != nil {
		return UploadResult{}, fmt.Errorf("uploading blocks: %w", err)
	}

	return UploadResult{
		RootCID: rootCID,
		URL:     fmt.Sprintf("https://storacha.link/ipfs/%s", rootCID.String()),
	}, nil
}

// UploadDirectory uploads a directory of files to the service and returns the root data CID.
// All files are added to a container directory, with paths in file names preserved.
// This mimics the JavaScript uploadDirectory function by directly calling client methods.
//
// Required delegated capability proofs: `space/blob/add`, `space/index/add`, `upload/add`, `filecoin/offer`
//
// Parameters:
//   - ctx: Context for cancellation and timeouts
//   - client: Storacha client with necessary capabilities
//   - spaceDID: The space DID to upload to
//   - dirPath: Path to the directory to upload
//   - opts: Optional upload options
//
// Returns the root CID of the uploaded directory.
func UploadDirectory(ctx context.Context, client *StorachaClient, spaceDID did.DID, dirPath string, opts *UploadOptions) (UploadResult, error) {
	if opts == nil {
		opts = &UploadOptions{Wrap: true}
	}

	// Encode directory as UnixFS blocks and create CAR
	blocks, rootCID, err := encodeDirectoryAsBlocks(ctx, dirPath)
	if err != nil {
		return UploadResult{}, fmt.Errorf("encoding directory as UnixFS: %w", err)
	}

	// Upload the blocks (creates CAR, uploads blob, registers index and upload)
	if err := uploadBlocks(ctx, client, spaceDID, blocks, rootCID, opts); err != nil {
		return UploadResult{}, fmt.Errorf("uploading blocks: %w", err)
	}

	return UploadResult{
		RootCID: rootCID,
		URL:     fmt.Sprintf("https://storacha.link/ipfs/%s", rootCID.String()),
	}, nil
}

// UploadCAR uploads a CAR file to the service.
// The CAR file is automatically sharded and an "upload" is registered,
// linking the individual shards.
// This mimics the JavaScript uploadCAR function.
//
// Required delegated capability proofs: `space/blob/add`, `space/index/add`, `upload/add`, `filecoin/offer`
//
// Note: This function is not yet fully implemented.
func UploadCAR(ctx context.Context, client *StorachaClient, spaceDID did.DID, carPath string, opts *UploadOptions) (UploadResult, error) {
	// TODO: Implement CAR upload
	// This would involve:
	// 1. Reading the CAR file
	// 2. Extracting blocks and roots
	// 3. Sharding if necessary
	// 4. Uploading shards via blob/add
	// 5. Creating indexes
	// 6. Registering upload via upload/add
	return UploadResult{}, fmt.Errorf("CAR upload not yet implemented")
}

// encodeFileAsBlocks encodes a file as UnixFS blocks
func encodeFileAsBlocks(ctx context.Context, file *os.File) ([]Block, cid.Cid, error) {
	var blocks []Block

	// Create a link system that captures blocks as they're created
	ls := createBlockCapturingLinkSystem(&blocks)

	// Build the UnixFS file using the standard builder
	// This uses 1MiB block size, matching the default
	link, _, err := builder.BuildUnixFSFile(file, "size-1048576", ls)
	if err != nil {
		return nil, cid.Undef, fmt.Errorf("building UnixFS file: %w", err)
	}

	rootCID := link.(cidlink.Link).Cid
	return blocks, rootCID, nil
}

// encodeDirectoryAsBlocks encodes a directory as UnixFS blocks
func encodeDirectoryAsBlocks(ctx context.Context, dirPath string) ([]Block, cid.Cid, error) {
	var blocks []Block

	// Create a link system that captures blocks as they're created
	ls := createBlockCapturingLinkSystem(&blocks)

	// Walk the directory to collect all entries
	type dirEntry struct {
		name    string
		absPath string
		isDir   bool
	}

	entries := make(map[string][]dirEntry) // parent path -> entries

	err := filepath.Walk(dirPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		// Skip the root directory itself
		if path == dirPath {
			return nil
		}

		relPath, err := filepath.Rel(dirPath, path)
		if err != nil {
			return err
		}

		parent := filepath.Dir(relPath)
		if parent == "." {
			parent = ""
		}

		entries[parent] = append(entries[parent], dirEntry{
			name:    filepath.Base(relPath),
			absPath: path,
			isDir:   info.IsDir(),
		})

		return nil
	})
	if err != nil {
		return nil, cid.Undef, fmt.Errorf("walking directory: %w", err)
	}

	// Build UnixFS DAG bottom-up
	dirLinks := make(map[string][]dagpb.PBLink) // directory path -> links

	// Sort directories by depth (deepest first)
	var dirPaths []string
	for path := range entries {
		dirPaths = append(dirPaths, path)
	}
	sort.Slice(dirPaths, func(i, j int) bool {
		return filepath.Dir(dirPaths[i]) > filepath.Dir(dirPaths[j])
	})

	// Process each directory
	for _, dirPath := range dirPaths {
		dirEntries := entries[dirPath]

		// Sort entries by name for determinism
		sort.Slice(dirEntries, func(i, j int) bool {
			return dirEntries[i].name < dirEntries[j].name
		})

		var links []dagpb.PBLink

		for _, entry := range dirEntries {
			var entryLink cidlink.Link
			var entrySize int64

			if entry.isDir {
				// Build directory node from collected links
				subDirPath := filepath.Join(dirPath, entry.name)
				if dirPath == "" {
					subDirPath = entry.name
				}

				subLinks := dirLinks[subDirPath]
				link, size, err := builder.BuildUnixFSDirectory(subLinks, ls)
				if err != nil {
					return nil, cid.Undef, fmt.Errorf("building directory %s: %w", entry.name, err)
				}
				entryLink = link.(cidlink.Link)
				entrySize = int64(size)
			} else {
				// Build file node
				file, err := os.Open(entry.absPath)
				if err != nil {
					return nil, cid.Undef, fmt.Errorf("opening file %s: %w", entry.name, err)
				}

				link, size, err := builder.BuildUnixFSFile(file, "size-1048576", ls)
				file.Close()
				if err != nil {
					return nil, cid.Undef, fmt.Errorf("building file %s: %w", entry.name, err)
				}
				entryLink = link.(cidlink.Link)
				entrySize = int64(size)
			}

			// Create PBLink for this entry
			pbLink, err := builder.BuildUnixFSDirectoryEntry(entry.name, entrySize, entryLink)
			if err != nil {
				return nil, cid.Undef, fmt.Errorf("building directory entry %s: %w", entry.name, err)
			}
			links = append(links, pbLink)
		}

		dirLinks[dirPath] = links
	}

	// Build root directory
	rootLinks := dirLinks[""]
	rootLink, _, err := builder.BuildUnixFSDirectory(rootLinks, ls)
	if err != nil {
		return nil, cid.Undef, fmt.Errorf("building root directory: %w", err)
	}

	rootCID := rootLink.(cidlink.Link).Cid
	return blocks, rootCID, nil
}

// uploadBlocks uploads a set of blocks to the service, mimicking the JS uploadBlocks function.
// It creates a CAR file, uploads it as a blob, creates an index, and registers the upload.
func uploadBlocks(ctx context.Context, client *StorachaClient, spaceDID did.DID, blocks []Block, rootCID cid.Cid, opts *UploadOptions) error {
	// Step 1: Create CAR file from blocks
	carBytes, err := encodeBlocksAsCAR(blocks, rootCID)
	if err != nil {
		return fmt.Errorf("encoding blocks as CAR: %w", err)
	}

	// Step 2: Compute digest and piece CID
	carDigest := sha256.Sum256(carBytes)
	carMultihash, err := multihash.Encode(carDigest[:], multihash.SHA2_256)
	if err != nil {
		return fmt.Errorf("encoding CAR multihash: %w", err)
	}
	carCID := cid.NewCidV1(uint64(0x0202), carMultihash) // 0x0202 is CAR codec
	carSize := uint64(len(carBytes))

	// Compute piece CID for Filecoin
	var pieceCID cid.Cid
	if carSize >= MinPiecePayload {
		commpCalc := &commp.Calc{}
		if _, err := commpCalc.Write(carBytes); err != nil {
			return fmt.Errorf("computing piece CID: %w", err)
		}
		pieceDigest := commpCalc.Sum(nil)
		pieceCID, err = commcid.DataCommitmentToPieceCidv2(pieceDigest, carSize)
		if err != nil {
			return fmt.Errorf("converting to piece CID: %w", err)
		}
	}

	// Step 3: Upload CAR as blob
	carReader := bytes.NewReader(carBytes)
	addedBlob, err := client.SpaceBlobAdd(ctx, carReader, spaceDID, carMultihash, carSize, opts)
	if err != nil {
		return fmt.Errorf("uploading CAR blob: %w", err)
	}

	// Step 4: Offer to Filecoin (if piece CID was computed)
	if pieceCID.Defined() && addedBlob.PDPAccept != nil {
		_, err = client.FilecoinOffer(ctx, spaceDID, cidlink.Link{Cid: carCID}, cidlink.Link{Cid: pieceCID}, addedBlob.PDPAccept)
		if err != nil {
			return fmt.Errorf("offering to Filecoin: %w", err)
		}
	}

	// Step 5: Create sharded DAG index
	indexBytes, err := createShardedDAGIndex(rootCID, carCID, carMultihash, carSize, blocks)
	if err != nil {
		return fmt.Errorf("creating sharded DAG index: %w", err)
	}

	// Step 6: Upload index
	indexDigest := sha256.Sum256(indexBytes)
	indexMultihash, err := multihash.Encode(indexDigest[:], multihash.SHA2_256)
	if err != nil {
		return fmt.Errorf("encoding index multihash: %w", err)
	}
	indexCID := cid.NewCidV1(uint64(0x0202), indexMultihash)

	indexReader := bytes.NewReader(indexBytes)
	_, err = client.SpaceBlobAdd(ctx, indexReader, spaceDID, indexMultihash, uint64(len(indexBytes)), nil)
	if err != nil {
		return fmt.Errorf("uploading index blob: %w", err)
	}

	// Step 7: Register index with the service
	err = client.SpaceIndexAdd(ctx, indexCID, uint64(len(indexBytes)), rootCID, spaceDID)
	if err != nil {
		return fmt.Errorf("registering index: %w", err)
	}

	// Step 8: Register upload with the service
	shards := []ipld.Link{cidlink.Link{Cid: carCID}}
	_, err = client.UploadAdd(ctx, spaceDID, cidlink.Link{Cid: rootCID}, shards)
	if err != nil {
		return fmt.Errorf("registering upload: %w", err)
	}

	return nil
}

// Helper functions

// createBlockCapturingLinkSystem creates a LinkSystem that captures blocks as they're encoded
func createBlockCapturingLinkSystem(blocks *[]Block) *linking.LinkSystem {
	ls := cidlink.DefaultLinkSystem()
	originalChooser := ls.EncoderChooser

	// No-op storage - we capture blocks in the encoder
	ls.StorageWriteOpener = func(lc linking.LinkContext) (io.Writer, linking.BlockWriteCommitter, error) {
		return io.Discard, func(l datamodel.Link) error { return nil }, nil
	}

	ls.EncoderChooser = func(lp datamodel.LinkPrototype) (codec.Encoder, error) {
		originalEncode, err := originalChooser(lp)
		if err != nil {
			return nil, err
		}

		codec := lp.(cidlink.LinkPrototype).Codec

		return func(node datamodel.Node, w io.Writer) error {
			// Encode the node and compute its CID
			var buf bytes.Buffer
			hasher := sha256.New()
			mw := io.MultiWriter(&buf, w, hasher)

			if err := originalEncode(node, mw); err != nil {
				return fmt.Errorf("encoding node: %w", err)
			}

			data := buf.Bytes()
			hash := hasher.Sum(nil)
			mh, err := multihash.Encode(hash, multihash.SHA2_256)
			if err != nil {
				return fmt.Errorf("encoding multihash: %w", err)
			}

			blockCID := cid.NewCidV1(codec, mh)

			// Capture the block
			*blocks = append(*blocks, Block{
				CID:  blockCID,
				Data: data,
			})

			return nil
		}, nil
	}

	return &ls
}

// encodeBlocksAsCAR encodes blocks as a CAR file
func encodeBlocksAsCAR(blocks []Block, rootCID cid.Cid) ([]byte, error) {
	var buf bytes.Buffer

	// Write CAR header with no roots (matching JS behavior)
	header := ipldcar.CarHeader{
		Roots:   nil,
		Version: 1,
	}

	headerBytes, err := cbor.DumpObject(header)
	if err != nil {
		return nil, fmt.Errorf("marshaling CAR header: %w", err)
	}

	if err := util.LdWrite(&buf, headerBytes); err != nil {
		return nil, fmt.Errorf("writing CAR header: %w", err)
	}

	// Write each block
	for _, block := range blocks {
		// Write block as: varint(cid_len + data_len) + cid + data
		cidBytes := block.CID.Bytes()
		blockLen := len(cidBytes) + len(block.Data)

		varintBuf := make([]byte, binary.MaxVarintLen64)
		n := varint.PutUvarint(varintBuf, uint64(blockLen))

		if _, err := buf.Write(varintBuf[:n]); err != nil {
			return nil, fmt.Errorf("writing block varint: %w", err)
		}
		if _, err := buf.Write(cidBytes); err != nil {
			return nil, fmt.Errorf("writing block CID: %w", err)
		}
		if _, err := buf.Write(block.Data); err != nil {
			return nil, fmt.Errorf("writing block data: %w", err)
		}
	}

	return buf.Bytes(), nil
}

// createShardedDAGIndex creates a sharded DAG index from blocks
func createShardedDAGIndex(rootCID, shardCID cid.Cid, shardDigest multihash.Multihash, shardSize uint64, blocks []Block) ([]byte, error) {
	// Create index view
	indexView := blobindex.NewShardedDagIndexView(cidlink.Link{Cid: rootCID}, -1)

	// Create slice map for this shard
	shardSlices := blobindex.NewMultihashMap[blobindex.Position](-1)

	// Calculate offset for each block in the CAR
	// CAR header with no roots
	headerBytes, _ := cbor.DumpObject(ipldcar.CarHeader{Roots: nil, Version: 1})
	varintBuf := make([]byte, binary.MaxVarintLen64)
	n := varint.PutUvarint(varintBuf, uint64(len(headerBytes)))
	offset := uint64(n + len(headerBytes))

	// Add each block to the index
	for _, block := range blocks {
		cidBytes := block.CID.Bytes()
		blockLen := len(cidBytes) + len(block.Data)

		// Varint for block length
		n := varint.PutUvarint(varintBuf, uint64(blockLen))
		offset += uint64(n)

		// CID bytes
		offset += uint64(len(cidBytes))

		// Record position (offset points to start of data, length is data size)
		shardSlices.Set(block.CID.Hash(), blobindex.Position{
			Offset: offset,
			Length: uint64(len(block.Data)),
		})

		// Move offset past the data
		offset += uint64(len(block.Data))
	}

	// Add the CAR shard itself to the index
	shardSlices.Set(shardDigest, blobindex.Position{
		Offset: 0,
		Length: shardSize,
	})

	// Set shard slices in index
	indexView.Shards().Set(shardDigest, shardSlices)

	// Archive the index as CAR
	archReader, err := blobindex.Archive(indexView)
	if err != nil {
		return nil, fmt.Errorf("archiving index: %w", err)
	}

	indexBytes, err := io.ReadAll(archReader)
	if err != nil {
		return nil, fmt.Errorf("reading index bytes: %w", err)
	}

	return indexBytes, nil
}
