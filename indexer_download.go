package go_storacha_upload_client_kit

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/ipfs/go-cid"
	"github.com/ipld/go-ipld-prime"
	cidlink "github.com/ipld/go-ipld-prime/linking/cid"
	contentcap "github.com/storacha/go-libstoracha/capabilities/space/content"
	"github.com/storacha/go-ucanto/core/delegation"
	"github.com/storacha/go-ucanto/did"
	"github.com/storacha/go-ucanto/ucan"
	"github.com/storacha/guppy/pkg/agentstore"
	"github.com/storacha/guppy/pkg/client/locator"
)

// DownloadFileViaIndexer downloads a UnixFS file by querying the Storacha indexing
// service for each block's exact shard location, then fetching each block via an
// HTTP range request against the shard blob URL. This is more direct and reliable
// than using an IPFS gateway.
func DownloadFileViaIndexer(ctx context.Context, client *StorachaClient, spaceDID did.DID, rootCID cid.Cid, outputPath string, opts *DownloadOptions) error {
	if opts == nil {
		opts = &DownloadOptions{}
	}
	lsys := createIndexerLinkSystem(ctx, client, spaceDID)
	return extractFileWithLinkSystem(lsys, rootCID, outputPath)
}

// DownloadDirectoryViaIndexer downloads a UnixFS directory by querying the Storacha
// indexing service for each block's exact shard location, then fetching blocks via
// HTTP range requests. This is more direct and reliable than using an IPFS gateway.
func DownloadDirectoryViaIndexer(ctx context.Context, client *StorachaClient, spaceDID did.DID, rootCID cid.Cid, outputDir string, opts *DownloadOptions) error {
	if opts == nil {
		opts = &DownloadOptions{}
	}
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("creating output directory: %w", err)
	}
	lsys := createIndexerLinkSystem(ctx, client, spaceDID)
	return extractDirectoryWithLinkSystem(ctx, lsys, rootCID, outputDir)
}

// createIndexerLinkSystem builds an IPLD LinkSystem where each block read:
//  1. Queries the Storacha indexer to find which shard blob contains the block
//     and at what byte offset/length.
//  2. Issues an HTTP range request (Range: bytes=<offset>-<offset+length-1>)
//     against the shard blob URL.
//  3. Returns the raw block bytes directly (the index stores data-level offsets,
//     so the range response contains raw block data with no CARv1 frame wrapper).
func createIndexerLinkSystem(ctx context.Context, client *StorachaClient, spaceDID did.DID) *ipld.LinkSystem {
	indexer, indexerPrincipal := MustGetIndexClient()
	loc := locator.NewIndexLocator(indexer, makeAuthFunc(client, indexerPrincipal))

	lsys := cidlink.DefaultLinkSystem()
	lsys.TrustedStorage = true
	lsys.StorageReadOpener = func(lc ipld.LinkContext, l ipld.Link) (io.Reader, error) {
		cl, ok := l.(cidlink.Link)
		if !ok {
			return nil, fmt.Errorf("not a CID link: %v", l)
		}

		reqCtx := lc.Ctx
		if reqCtx == nil {
			reqCtx = ctx
		}

		var err error
		// The locator uses a single-pass cache: the first Locate call populates
		// shard inclusions (which shard contains the block), but the shard's own
		// location URL may only be resolved on a second call (once inclusions are
		// cached and drive a targeted shard-location query). Retry once on
		// NotFoundError to let the second pass complete.
		var locations []locator.Location
		for attempt := 1; attempt <= 3; attempt++ {
			fmt.Printf("Attempt %d to locate block %s\n", attempt, cl.Cid)
			locations, err = loc.Locate(reqCtx, []did.DID{spaceDID}, cl.Cid.Hash())
			if err == nil {
				break
			}
			var notFound locator.NotFoundError
			if !errors.As(err, &notFound) {
				break // real error, don't retry
			}
			<-time.After(1 * time.Second * time.Duration(attempt))
		}
		if err != nil {
			return nil, fmt.Errorf("locating block %s: %w", cl.Cid, err)
		}
		if len(locations) == 0 {
			return nil, fmt.Errorf("no locations found for block %s", cl.Cid)
		}

		location := locations[0]
		if len(location.Commitment.Nb().Location) == 0 {
			return nil, fmt.Errorf("no URLs in location for block %s", cl.Cid)
		}

		shardURL := location.Commitment.Nb().Location[0].String()
		offset := location.Position.Offset
		length := location.Position.Length

		// Issue a targeted HTTP range request to fetch just this block's raw data.
		req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, shardURL, nil)
		if err != nil {
			return nil, fmt.Errorf("creating range request: %w", err)
		}
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", offset, offset+length-1))

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("fetching block %s: %w", cl.Cid, err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusPartialContent && resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("unexpected status %d fetching block %s", resp.StatusCode, cl.Cid)
		}

		frame, err := io.ReadAll(resp.Body)
		if err != nil {
			return nil, fmt.Errorf("reading block frame for %s: %w", cl.Cid, err)
		}

		return bytes.NewReader(frame), nil
	}

	return &lsys
}

// makeAuthFunc returns a locator.AuthorizeRetrievalFunc that creates a short-lived
// delegation allowing the indexing service to retrieve indexes from the given spaces.
func makeAuthFunc(client *StorachaClient, indexerPrincipal ucan.Principal) locator.AuthorizeRetrievalFunc {
	return func(spaces []did.DID) (delegation.Delegation, error) {
		queries := make([]agentstore.CapabilityQuery, 0, len(spaces))
		for _, space := range spaces {
			queries = append(queries, agentstore.CapabilityQuery{
				Can:  contentcap.Retrieve.Can(),
				With: space.String(),
			})
		}

		var pfs []delegation.Proof
		res, err := client.Proofs(queries...)
		if err != nil {
			return nil, fmt.Errorf("getting proofs: %w", err)
		}
		if len(res) > 0 {
			pfs = append(pfs, delegation.FromDelegation(res[0]))
		}

		caps := make([]ucan.Capability[ucan.NoCaveats], 0, len(spaces))
		for _, space := range spaces {
			caps = append(caps, ucan.NewCapability(
				contentcap.Retrieve.Can(),
				space.DID().String(),
				ucan.NoCaveats{},
			))
		}

		return delegation.Delegate(
			client.Issuer(),
			indexerPrincipal,
			caps,
			delegation.WithProof(pfs...),
			delegation.WithExpiration(int(time.Now().Add(30*time.Second).Unix())),
		)
	}
}
