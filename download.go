// Package go_storacha_upload_client_kit provides a library for downloading files and directories from Storacha.
package go_storacha_upload_client_kit

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	lite "github.com/hsanjuan/ipfs-lite"
	"github.com/ipfs/boxo/bootstrap"
	"github.com/ipfs/boxo/peering"
	"github.com/ipfs/go-cid"
	"github.com/ipfs/go-unixfsnode"
	ipldcar "github.com/ipld/go-car"
	dagpb "github.com/ipld/go-codec-dagpb"
	"github.com/ipld/go-ipld-prime"
	"github.com/ipld/go-ipld-prime/datamodel"
	cidlink "github.com/ipld/go-ipld-prime/linking/cid"
	"github.com/ipld/go-ipld-prime/node/basicnode"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
	"github.com/multiformats/go-varint"
)

const defaultGateway = "ipfs.w3s.link"

// DownloadOptions contains options for download functions.
type DownloadOptions struct {
	// OnProgress is called with download progress updates.
	OnProgress func(downloaded int64)
	// Gateway overrides the default IPFS HTTP gateway (default: https://w3s.link).
	Gateway string
}

// bootstrapPeers are the multiaddrs of known bootstrap nodes used to join the IPFS network.
var bootstrapPeerAddrs = []string{
	"/dns4/bootstrap-0.ipfsmain.cn/tcp/34721/p2p/12D3KooWQnwEGNqcM2nAcPtRR9rAX8Hrg4k9kJLCHoTR5chJfz6d",
	"/dns4/bootstrap-6.mainnet.filops.net/tcp/1347/p2p/12D3KooWP5MwCiqdMETF9ub1P3MbCvQCcfconnYHbWg6sUJcDRQQ",
	"/dns4/bootstrap-7.mainnet.filops.net/tcp/1347/p2p/12D3KooWRs3aY1p3juFjPy8gPN95PEQChm2QKGUCAdcDCC4EBMKf",
	"/dns4/bootstrap-8.mainnet.filops.net/tcp/1347/p2p/12D3KooWScFR7385LTyR4zU1bYdzSiiAb5rnNABfVahPvVSzyTkR",
	"/dns4/node.glif.io/tcp/1235/p2p/12D3KooWBF8cpp65hp2u9LK5mh19x67ftAam84z9LsfaquTDSBpt",
	"/dns4/bootstrap-2.mainnet.filops.net/tcp/1347/p2p/12D3KooWEWVwHGn2yR36gKLozmb4YjDJGerotAPGxmdWZx2nxMC4",
	"/dns4/bootstrap-1.starpool.in/tcp/12757/p2p/12D3KooWQZrGH1PxSNZPum99M1zNvjNFM33d1AAu5DcvdHptuU7u",
	"/dns4/bootstrap-0.mainnet.filops.net/tcp/1347/p2p/12D3KooWCVe8MmsEMes2FzgTpt9fXtmCY7wrq91GRiaC8PHSCCBj",
	"/dns4/bootstrap-1.mainnet.filops.net/tcp/1347/p2p/12D3KooWCwevHg1yLCvktf2nvLu7L9894mcrJR4MsBCcm4syShVc",
	"/dns4/bootstrap-0.starpool.in/tcp/12757/p2p/12D3KooWGHpBMeZbestVEWkfdnC9u7p6uFHXL1n7m1ZBqsEmiUzz",
	"/dns4/lotus-bootstrap.ipfsforce.com/tcp/41778/p2p/12D3KooWGhufNmZHF3sv48aQeS13ng5XVJZ9E6qy2Ms4VzqeUsHk",
	"/dns4/bootstrap-1.ipfsmain.cn/tcp/34723/p2p/12D3KooWMKxMkD5DMpSWsW7dBddKxKT7L2GgbNuckz9otxvkvByP",
}

// bootstrapPeers parses bootstrapPeerAddrs into peer.AddrInfo values, skipping any that fail to parse.
func bootstrapPeers() []peer.AddrInfo {
	infos := make([]peer.AddrInfo, 0, len(bootstrapPeerAddrs))
	for _, addr := range bootstrapPeerAddrs {
		ma, err := multiaddr.NewMultiaddr(addr)
		if err != nil {
			continue
		}
		ai, err := peer.AddrInfoFromP2pAddr(ma)
		if err != nil {
			continue
		}
		infos = append(infos, *ai)
	}
	return infos
}

// createIPFSPeer creates a new ipfs-lite peer connected to the IPFS network.
func createIPFSPeer(ctx context.Context) (*lite.Peer, error) {
	ds := lite.NewInMemoryDatastore()

	priv, _, err := crypto.GenerateKeyPair(crypto.Ed25519, -1)
	if err != nil {
		return nil, fmt.Errorf("generating key pair: %w", err)
	}

	listen, err := multiaddr.NewMultiaddr("/ip4/0.0.0.0/tcp/0")
	if err != nil {
		return nil, fmt.Errorf("parsing listen addr: %w", err)
	}

	h, dht, err := lite.SetupLibp2p(
		ctx,
		priv,
		nil,
		[]multiaddr.Multiaddr{listen},
		ds,
		lite.Libp2pOptionsExtra...,
	)
	if err != nil {
		return nil, fmt.Errorf("setting up libp2p: %w", err)
	}

	p, err := lite.New(ctx, ds, nil, h, dht, nil)
	if err != nil {
		return nil, fmt.Errorf("creating IPFS peer: %w", err)
	}

	peers := bootstrapPeers()

	// Start the boxo bootstrap service to maintain minimum peer connections.
	bootCfg := bootstrap.BootstrapConfigWithPeers(peers)
	bootCfg.MinPeerThreshold = len(peers)
	bootstrapper, err := bootstrap.Bootstrap(h.ID(), h, nil, bootCfg)
	if err != nil {
		return nil, fmt.Errorf("starting bootstrap service: %w", err)
	}

	// Start the peering service to maintain persistent connections to known peers.
	peeringSvc := peering.NewPeeringService(h)
	for _, ai := range peers {
		peeringSvc.AddPeer(ai)
	}
	if err := peeringSvc.Start(); err != nil {
		bootstrapper.Close()
		return nil, fmt.Errorf("starting peering service: %w", err)
	}

	p.Bootstrap(peers)

	// Clean up both services when the context is cancelled.
	go func() {
		<-ctx.Done()
		bootstrapper.Close()
		peeringSvc.Stop()
	}()

	return p, nil
}

// createLinkSystemFromPeer builds an IPLD LinkSystem whose block reads are
// fulfilled by the given ipfs-lite peer over the IPFS network.
func createLinkSystemFromPeer(p *lite.Peer) *ipld.LinkSystem {
	lsys := cidlink.DefaultLinkSystem()
	lsys.TrustedStorage = true
	lsys.StorageReadOpener = func(lc ipld.LinkContext, l ipld.Link) (io.Reader, error) {
		cl, ok := l.(cidlink.Link)
		if !ok {
			return nil, fmt.Errorf("not a CID link: %v", l)
		}
		ctx := lc.Ctx
		if ctx == nil {
			ctx = context.Background()
		}
		node, err := p.Get(ctx, cl.Cid)
		if err != nil {
			return nil, fmt.Errorf("getting block %s: %w", cl.Cid, err)
		}
		return bytes.NewReader(node.RawData()), nil
	}
	return &lsys
}

// DownloadFile downloads a UnixFS file by its root CID via an IPFS HTTP gateway.
// The default gateway is https://w3s.link; override with DownloadOptions.Gateway.
//
// If the CID resolves to a directory rather than a file, an error is returned so
// the caller can fall back to DownloadDirectory.
//
// Parameters:
//   - ctx: Context for cancellation and timeouts
//   - rootCID: The root CID of the file to download
//   - outputPath: Path where the file should be saved
//   - opts: Optional download options
func DownloadFile(ctx context.Context, rootCID cid.Cid, outputPath string, opts *DownloadOptions) error {
	if opts == nil {
		opts = &DownloadOptions{}
	}

	gateway := defaultGateway
	if opts.Gateway != "" {
		gateway = opts.Gateway
	}

	url := fmt.Sprintf("https://%s.%s", rootCID.String(), gateway)
	log.Println("Downloading file from:", url)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("fetching from gateway: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("gateway returned %d for CID %s", resp.StatusCode, rootCID)
	}

	// A directory CID returns an HTML listing — signal the caller to try DownloadDirectory.
	if strings.Contains(resp.Header.Get("Content-Type"), "text/html") {
		return fmt.Errorf("CID %s is a directory, not a file", rootCID)
	}

	// check outputPath is a directory
	if isDir, err := isDirectory(outputPath); err != nil {
		return fmt.Errorf("checking if output path is a directory: %w", err)
	} else if isDir {
		outputPath = filepath.Join(outputPath, rootCID.String())
	}

	outFile, err := os.Create(outputPath)
	if err != nil {
		return fmt.Errorf("creating output file: %w", err)
	}
	defer outFile.Close()

	var src io.Reader = resp.Body
	if opts.OnProgress != nil {
		src = &progressReader{r: resp.Body, progress: opts.OnProgress}
	}

	if _, err := io.Copy(outFile, src); err != nil {
		return fmt.Errorf("writing file: %w", err)
	}

	return nil
}

// isDirectory checks if the path exists and is a directory.
func isDirectory(path string) (bool, error) {
	fileInfo, err := os.Stat(path)
	if err != nil {
		// Return false and the error (e.g., if the path does not exist,
		// or for permission issues).
		return false, err
	}
	// Check if the mode of the file info is a directory.
	return fileInfo.IsDir(), nil
}

// DownloadDirectory downloads a UnixFS directory by its root CID via an IPFS HTTP gateway.
// The gateway is asked for a CAR export of the full DAG which is then reconstructed locally.
// The default gateway is https://w3s.link; override with DownloadOptions.Gateway.
//
// Parameters:
//   - ctx: Context for cancellation and timeouts
//   - rootCID: The root CID of the directory to download
//   - outputDir: Path where the directory should be saved
//   - opts: Optional download options
func DownloadDirectory(ctx context.Context, rootCID cid.Cid, outputDir string, opts *DownloadOptions) error {
	if opts == nil {
		opts = &DownloadOptions{}
	}

	gateway := defaultGateway
	if opts.Gateway != "" {
		gateway = opts.Gateway
	}

	url := fmt.Sprintf("%s/ipfs/%s?format=car", gateway, rootCID.String())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.ipld.car")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("fetching CAR from gateway: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("gateway returned %d for CID %s", resp.StatusCode, rootCID)
	}

	var r io.Reader = resp.Body
	if opts.OnProgress != nil {
		r = &progressReader{r: resp.Body, progress: opts.OnProgress}
	}

	carData, err := io.ReadAll(r)
	if err != nil {
		return fmt.Errorf("reading CAR data: %w", err)
	}

	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("creating output directory: %w", err)
	}

	blocks, _, err := ExtractCARBlocks(carData)
	if err != nil {
		return fmt.Errorf("extracting CAR blocks: %w", err)
	}

	lsys := CreateLinkSystemFromBlocks(blocks)
	return extractDirectoryWithLinkSystem(ctx, lsys, rootCID, outputDir)
}

// ReconstructFileFromCAR reconstructs a file from a CAR file containing UnixFS blocks.
//
// Parameters:
//   - carPath: Path to the CAR file
//   - rootCID: The root CID of the file to extract
//   - outputPath: Path where the file should be saved
func ReconstructFileFromCAR(carPath string, rootCID cid.Cid, outputPath string) error {
	carData, err := os.ReadFile(carPath)
	if err != nil {
		return fmt.Errorf("reading CAR file: %w", err)
	}

	blocks, _, err := ExtractCARBlocks(carData)
	if err != nil {
		return fmt.Errorf("extracting blocks: %w", err)
	}

	lsys := CreateLinkSystemFromBlocks(blocks)
	return extractFileWithLinkSystem(lsys, rootCID, outputPath)
}

// ReconstructDirectoryFromCAR reconstructs a directory from a CAR file containing UnixFS blocks.
//
// Parameters:
//   - carPath: Path to the CAR file
//   - rootCID: The root CID of the directory to extract
//   - outputDir: Path where the directory should be saved
func ReconstructDirectoryFromCAR(carPath string, rootCID cid.Cid, outputDir string) error {
	carData, err := os.ReadFile(carPath)
	if err != nil {
		return fmt.Errorf("reading CAR file: %w", err)
	}

	blocks, _, err := ExtractCARBlocks(carData)
	if err != nil {
		return fmt.Errorf("extracting blocks: %w", err)
	}

	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("creating output directory: %w", err)
	}

	lsys := CreateLinkSystemFromBlocks(blocks)
	return extractDirectoryWithLinkSystem(context.Background(), lsys, rootCID, outputDir)
}

// extractFileWithLinkSystem extracts a UnixFS file using the given link system.
func extractFileWithLinkSystem(lsys *ipld.LinkSystem, rootCID cid.Cid, outputPath string) error {
	// If outputPath is an existing directory, save the file inside it using the CID as the filename.
	if info, err := os.Stat(outputPath); err == nil && info.IsDir() {
		outputPath = filepath.Join(outputPath, rootCID.String())
	}

	link := cidlink.Link{Cid: rootCID}
	node, err := lsys.Load(ipld.LinkContext{}, link, basicnode.Prototype.Any)
	if err != nil {
		return fmt.Errorf("loading root node: %w", err)
	}

	ufsNode, err := unixfsnode.Reify(ipld.LinkContext{}, node, lsys)
	if err != nil {
		return fmt.Errorf("reifying UnixFS node: %w", err)
	}

	if ufsNode.Kind() != datamodel.Kind_Bytes {
		return fmt.Errorf("node is not a file, kind: %s", ufsNode.Kind())
	}

	fileData, err := ufsNode.AsBytes()
	if err != nil {
		return fmt.Errorf("reading file data: %w", err)
	}

	if err := os.WriteFile(outputPath, fileData, 0644); err != nil {
		return fmt.Errorf("writing output file: %w", err)
	}

	return nil
}

// extractDirectoryWithLinkSystem extracts a UnixFS directory by traversing dagpb Links.
// It uses the raw dagpb link list for traversal to correctly handle reified UnixFS directories.
func extractDirectoryWithLinkSystem(ctx context.Context, lsys *ipld.LinkSystem, rootCID cid.Cid, outputDir string) error {
	// Load as a dagpb PBNode to get the raw Links list.
	pbNode, err := lsys.Load(ipld.LinkContext{Ctx: ctx}, cidlink.Link{Cid: rootCID}, dagpb.Type.PBNode)
	if err != nil {
		return fmt.Errorf("loading directory node %s: %w", rootCID, err)
	}

	linksNode, err := pbNode.LookupByString("Links")
	if err != nil {
		return fmt.Errorf("getting Links field: %w", err)
	}

	iter := linksNode.ListIterator()
	for !iter.Done() {
		_, linkNode, err := iter.Next()
		if err != nil {
			return fmt.Errorf("iterating links: %w", err)
		}

		nameNode, err := linkNode.LookupByString("Name")
		if err != nil {
			return fmt.Errorf("getting link Name: %w", err)
		}
		name, err := nameNode.AsString()
		if err != nil {
			return fmt.Errorf("reading link name: %w", err)
		}

		hashNode, err := linkNode.LookupByString("Hash")
		if err != nil {
			return fmt.Errorf("getting link Hash: %w", err)
		}
		lnk, err := hashNode.AsLink()
		if err != nil {
			return fmt.Errorf("reading link for %s: %w", name, err)
		}
		cl, ok := lnk.(cidlink.Link)
		if !ok {
			return fmt.Errorf("entry %s is not a CID link", name)
		}

		entryPath := filepath.Join(outputDir, name)

		// Determine whether the entry is a file or sub-directory by reifying it.
		entryRaw, err := lsys.Load(ipld.LinkContext{Ctx: ctx}, cl, basicnode.Prototype.Any)
		if err != nil {
			return fmt.Errorf("loading entry %s: %w", name, err)
		}
		ufsNode, err := unixfsnode.Reify(ipld.LinkContext{Ctx: ctx}, entryRaw, lsys)
		if err != nil {
			return fmt.Errorf("reifying entry %s: %w", name, err)
		}

		switch ufsNode.Kind() {
		case datamodel.Kind_Map:
			if err := os.MkdirAll(entryPath, 0755); err != nil {
				return fmt.Errorf("creating directory %s: %w", name, err)
			}
			if err := extractDirectoryWithLinkSystem(ctx, lsys, cl.Cid, entryPath); err != nil {
				return fmt.Errorf("extracting sub-directory %s: %w", name, err)
			}
		case datamodel.Kind_Bytes:
			fileData, err := ufsNode.AsBytes()
			if err != nil {
				return fmt.Errorf("reading file data for %s: %w", name, err)
			}
			if err := os.WriteFile(entryPath, fileData, 0644); err != nil {
				return fmt.Errorf("writing file %s: %w", name, err)
			}
		default:
			return fmt.Errorf("unexpected node kind for %s: %s", name, ufsNode.Kind())
		}
	}

	return nil
}

// ExtractCARBlocks extracts all blocks from a CAR file into a map.
func ExtractCARBlocks(carData []byte) (map[cid.Cid][]byte, []cid.Cid, error) {
	reader, err := ipldcar.NewCarReader(bytes.NewReader(carData))
	if err != nil {
		if err.Error() == "empty car, no roots" {
			return extractCARBlocksManually(carData)
		}
		return nil, nil, fmt.Errorf("creating CAR reader: %w", err)
	}

	blocks := make(map[cid.Cid][]byte)
	var roots []cid.Cid

	for _, root := range reader.Header.Roots {
		roots = append(roots, root)
	}

	for {
		block, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, nil, fmt.Errorf("reading block: %w", err)
		}
		blocks[block.Cid()] = block.RawData()
	}

	return blocks, roots, nil
}

// extractCARBlocksManually extracts blocks from a CAR with no roots.
func extractCARBlocksManually(carData []byte) (map[cid.Cid][]byte, []cid.Cid, error) {
	reader := bytes.NewReader(carData)
	bufReader := bufio.NewReader(reader)

	header, err := ipldcar.ReadHeader(bufReader)
	if err != nil {
		return nil, nil, fmt.Errorf("reading CAR header: %w", err)
	}

	blocks := make(map[cid.Cid][]byte)
	var roots []cid.Cid

	for _, root := range header.Roots {
		roots = append(roots, root)
	}

	for {
		length, err := varint.ReadUvarint(bufReader)
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, nil, fmt.Errorf("reading block length: %w", err)
		}

		blockBytes := make([]byte, length)
		if _, err := io.ReadFull(bufReader, blockBytes); err != nil {
			if err == io.EOF {
				break
			}
			return nil, nil, fmt.Errorf("reading block data: %w", err)
		}

		_, c, err := cid.CidFromBytes(blockBytes)
		if err != nil {
			return nil, nil, fmt.Errorf("parsing CID: %w", err)
		}

		cidLen := len(c.Bytes())
		blocks[c] = blockBytes[cidLen:]
	}

	return blocks, roots, nil
}

// ParseCARHeader parses a CAR file header and returns roots and blocks count.
func ParseCARHeader(carData []byte) ([]cid.Cid, int, error) {
	blocks, roots, err := ExtractCARBlocks(carData)
	if err != nil {
		return nil, 0, fmt.Errorf("extracting blocks: %w", err)
	}
	return roots, len(blocks), nil
}

// CreateBlockStore creates an in-memory block store from a CAR file.
func CreateBlockStore(carData []byte) (map[cid.Cid][]byte, error) {
	blocks, _, err := ExtractCARBlocks(carData)
	return blocks, err
}

// CreateLinkSystemFromBlocks creates an IPLD LinkSystem backed by the given block map.
func CreateLinkSystemFromBlocks(blocks map[cid.Cid][]byte) *ipld.LinkSystem {
	lsys := cidlink.DefaultLinkSystem()
	lsys.TrustedStorage = true
	lsys.StorageReadOpener = func(lc ipld.LinkContext, l ipld.Link) (io.Reader, error) {
		cl, ok := l.(cidlink.Link)
		if !ok {
			return nil, fmt.Errorf("not a CID link: %v", l)
		}
		data, ok := blocks[cl.Cid]
		if !ok {
			return nil, fmt.Errorf("block not found: %s", cl.Cid.String())
		}
		return bytes.NewReader(data), nil
	}
	return &lsys
}

// TraverseUnixFSFile traverses a UnixFS file and calls the callback for each chunk.
func TraverseUnixFSFile(blocks map[cid.Cid][]byte, rootCID cid.Cid, callback func(chunkData []byte) error) error {
	lsys := CreateLinkSystemFromBlocks(blocks)

	link := cidlink.Link{Cid: rootCID}
	node, err := lsys.Load(ipld.LinkContext{}, link, basicnode.Prototype.Any)
	if err != nil {
		return fmt.Errorf("loading root node: %w", err)
	}

	ufsNode, err := unixfsnode.Reify(ipld.LinkContext{}, node, lsys)
	if err != nil {
		return fmt.Errorf("reifying UnixFS node: %w", err)
	}

	if ufsNode.Kind() != datamodel.Kind_Bytes {
		return fmt.Errorf("node is not a file")
	}

	fileData, err := ufsNode.AsBytes()
	if err != nil {
		return fmt.Errorf("reading file data: %w", err)
	}

	return callback(fileData)
}

// GetUnixFSNodeInfo returns information about a UnixFS node from a block store.
func GetUnixFSNodeInfo(blocks map[cid.Cid][]byte, nodeCID cid.Cid) (string, int64, error) {
	lsys := CreateLinkSystemFromBlocks(blocks)

	link := cidlink.Link{Cid: nodeCID}
	node, err := lsys.Load(ipld.LinkContext{}, link, basicnode.Prototype.Any)
	if err != nil {
		return "", 0, fmt.Errorf("loading node: %w", err)
	}

	ufsNode, err := unixfsnode.Reify(ipld.LinkContext{}, node, lsys)
	if err != nil {
		return "", 0, fmt.Errorf("reifying UnixFS node: %w", err)
	}

	switch ufsNode.Kind() {
	case datamodel.Kind_Bytes:
		data, err := ufsNode.AsBytes()
		if err != nil {
			return "file", 0, nil
		}
		return "file", int64(len(data)), nil
	case datamodel.Kind_Map:
		iter := ufsNode.MapIterator()
		count := int64(0)
		for !iter.Done() {
			_, _, err := iter.Next()
			if err != nil {
				break
			}
			count++
		}
		return "directory", count, nil
	default:
		return ufsNode.Kind().String(), 0, nil
	}
}
