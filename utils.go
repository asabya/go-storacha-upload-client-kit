package go_storacha_upload_client_kit

import (
	"context"
	"errors"
	"fmt"
	"io"
	"iter"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/ipfs/go-cid"
	cidlink "github.com/ipld/go-ipld-prime/linking/cid"
	"github.com/ipld/go-ipld-prime/schema"
	"github.com/multiformats/go-multihash"
	blobcap "github.com/storacha/go-libstoracha/capabilities/blob"
	filecoincap "github.com/storacha/go-libstoracha/capabilities/filecoin"
	httpcap "github.com/storacha/go-libstoracha/capabilities/http"
	spaceblobcap "github.com/storacha/go-libstoracha/capabilities/space/blob"
	contentcap "github.com/storacha/go-libstoracha/capabilities/space/content"
	spaceindexcap "github.com/storacha/go-libstoracha/capabilities/space/index"
	captypes "github.com/storacha/go-libstoracha/capabilities/types"
	ucancap "github.com/storacha/go-libstoracha/capabilities/ucan"
	uploadcap "github.com/storacha/go-libstoracha/capabilities/upload"
	w3sblobcap "github.com/storacha/go-libstoracha/capabilities/web3.storage/blob"
	"github.com/storacha/go-libstoracha/failure"
	uclient "github.com/storacha/go-ucanto/client"
	rclient "github.com/storacha/go-ucanto/client/retrieval"
	"github.com/storacha/go-ucanto/core/dag/blockstore"
	"github.com/storacha/go-ucanto/core/delegation"
	"github.com/storacha/go-ucanto/core/invocation"
	"github.com/storacha/go-ucanto/core/ipld"
	"github.com/storacha/go-ucanto/core/receipt"
	"github.com/storacha/go-ucanto/core/receipt/fx"
	"github.com/storacha/go-ucanto/core/receipt/ran"
	"github.com/storacha/go-ucanto/core/result"
	resultfailure "github.com/storacha/go-ucanto/core/result/failure"
	"github.com/storacha/go-ucanto/did"
	"github.com/storacha/go-ucanto/principal"
	"github.com/storacha/go-ucanto/principal/ed25519/signer"
	"github.com/storacha/go-ucanto/transport/car"
	uhttp "github.com/storacha/go-ucanto/transport/http"
	"github.com/storacha/go-ucanto/ucan"
	"github.com/storacha/go-ucanto/validator"
	"github.com/storacha/guppy/pkg/agentstore"
	"github.com/storacha/guppy/pkg/client/locator"
	cdg "github.com/storacha/guppy/pkg/delegation"
	receiptclient "github.com/storacha/guppy/pkg/receipt"
	indexclient "github.com/storacha/indexing-service/pkg/client"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

const defaultServiceName = "up.storacha.network"
const defaultIndexerName = "indexer.storacha.network"
const defaultIndexerDID = "did:key:z6MkqMSJxrjzvpqmP3kZhk7eCasBK6DX1jaVaG7wD72LYRm7"

// envSigner returns a principal.Signer from the environment variable
// GUPPY_PRIVATE_KEY, if any.
func envSigner() (principal.Signer, error) {
	str := os.Getenv("GUPPY_PRIVATE_KEY") // use env var preferably
	if str == "" {
		return nil, nil // no signer in the environment
	}

	return signer.Parse(str)
}

// Create a transport with compression disabled to prevent Accept-Encoding header
// from being added automatically, which would break AWS S3 pre-signed URL signatures
var baseTransport = &http.Transport{
	DisableCompression: true,
}

var tracedHttpClient = &http.Client{
	Transport: otelhttp.NewTransport(baseTransport),
}

func MustGetConnection() uclient.Connection {
	// service URL & DID
	serviceURLStr := os.Getenv("STORACHA_SERVICE_URL") // use env var preferably
	if serviceURLStr == "" {
		serviceURLStr = fmt.Sprintf("https://%s", defaultServiceName)
	}

	serviceURL, err := url.Parse(serviceURLStr)
	if err != nil {
		log.Fatal(err)
	}

	serviceDIDStr := os.Getenv("STORACHA_SERVICE_DID")
	if serviceDIDStr == "" {
		serviceDIDStr = fmt.Sprintf("did:web:%s", defaultServiceName)
	}

	servicePrincipal, err := did.Parse(serviceDIDStr)
	if err != nil {
		log.Fatal(err)
	}

	// HTTP transport and CAR encoding
	channel := uhttp.NewChannel(serviceURL, uhttp.WithClient(tracedHttpClient))
	codec := car.NewOutboundCodec()

	conn, err := uclient.NewConnection(servicePrincipal, channel, uclient.WithOutboundCodec(codec))
	if err != nil {
		log.Fatal(err)
	}

	return conn
}

func MustGetReceiptsURL() *url.URL {
	receiptsURLStr := os.Getenv("STORACHA_RECEIPTS_URL")
	if receiptsURLStr == "" {
		receiptsURLStr = fmt.Sprintf("https://%s/receipt", defaultServiceName)
	}

	receiptsURL, err := url.Parse(receiptsURLStr)
	if err != nil {
		log.Fatal(err)
	}

	return receiptsURL
}

func MustGetIndexClient() (*indexclient.Client, ucan.Principal) {
	indexerURLStr := os.Getenv("STORACHA_INDEXING_SERVICE_URL") // use env var preferably
	if indexerURLStr == "" {
		indexerURLStr = fmt.Sprintf("https://%s", defaultIndexerName)
	}

	indexerURL, err := url.Parse(indexerURLStr)
	if err != nil {
		log.Fatal(err)
	}

	indexerDIDStr := os.Getenv("STORACHA_INDEXING_SERVICE_DID")
	if indexerDIDStr == "" {
		indexerDIDStr = defaultIndexerDID
	}

	indexerPrincipal, err := did.Parse(indexerDIDStr)
	if err != nil {
		log.Fatal(err)
	}

	client, err := indexclient.New(indexerPrincipal, *indexerURL, indexclient.WithHTTPClient(tracedHttpClient))
	if err != nil {
		log.Fatal(err)
	}

	return client, indexerPrincipal
}

func MustGetProof(path string) delegation.Delegation {
	b, err := os.ReadFile(path)
	if err != nil {
		log.Fatalf("reading proof file: %s", err)
	}

	proof, err := cdg.ExtractProof(b)
	if err != nil {
		log.Fatalf("extracting proof: %s", err)
	}
	return proof
}

// GenerateSigner creates a new Ed25519 signing key for use as the client principal.
// The returned signer can be passed to client.SetPrincipal().
//
// Example:
//
//	signer, err := GenerateSigner()
//	if err != nil {
//	    log.Fatal(err)
//	}
//	fmt.Printf("Generated DID: %s\n", signer.DID())
//	client.SetPrincipal(signer)
func GenerateSigner() (principal.Signer, error) {
	return signer.Generate()
}

// ParseSize parses a data size string with optional suffix (B, K, M, G).
// Accepts formats like: "1024", "512B", "100K", "50M", "2G". Digits with no
// suffix are interpreted as bytes. Returns the size in bytes.
func ParseSize(s string) (uint64, error) {
	if s == "" {
		return 0, errors.New("data size cannot be empty")
	}

	// Trim any whitespace
	s = strings.TrimSpace(s)

	// Check if it ends with a suffix
	var multiplier uint64 = 1
	var numStr string

	lastChar := strings.ToUpper(s[len(s)-1:])
	switch lastChar {
	case "B":
		multiplier = 1
		numStr = s[:len(s)-1]
	case "K":
		multiplier = 1024
		numStr = s[:len(s)-1]
	case "M":
		multiplier = 1024 * 1024
		numStr = s[:len(s)-1]
	case "G":
		multiplier = 1024 * 1024 * 1024
		numStr = s[:len(s)-1]
	default:
		// No suffix, assume bytes
		numStr = s
	}

	// Parse the numeric part
	num, err := strconv.ParseUint(numStr, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid shard size format: %w", err)
	}

	// Calculate the final size
	size := num * multiplier

	return size, nil
}

func NewHandledCliError(err error) HandledCliError {
	return HandledCliError{err}
}

// HandledCliError is an error which has already been presented to the user. If
// a HandledCliError is returned from a command, the process should exit with
// a non-zero exit code, but no further error message should be printed.
type HandledCliError struct {
	error
}

func (e HandledCliError) Unwrap() error {
	return e.error
}

// StorachaClient provides the main interface for interacting with Storacha.
// It handles authentication, uploads, and downloads using UCAN capabilities.
//
// Create a client using NewStorachaClientFromW3Access for JS CLI compatibility,
// or NewStorachaClient for native guppy format.
//
// The client requires:
//   - A principal (signing key) - set via SetPrincipal or loaded from store
//   - Delegations granting capabilities - obtained via Login flow or loaded from store
type StorachaClient struct {
	connection       uclient.Connection
	receiptsClient   *receiptclient.Client
	store            agentstore.Store
	putClient        *http.Client
	retrievalClient  *http.Client
	additionalProofs []delegation.Delegation
}

// AddedBlob represents the result of adding a blob to Storacha
type AddedBlob struct {
	Digest    multihash.Multihash
	Size      uint64
	Location  invocation.Invocation
	PDPAccept invocation.Invocation
}

// NewStorachaClient creates a new Storacha client using the native guppy store format.
// For JS CLI compatibility, use NewStorachaClientFromW3Access instead.
//
// The storePath should be a directory path where agent data will be stored.
// If GUPPY_PRIVATE_KEY environment variable is set, it overrides the stored principal.
func NewStorachaClient(storePath string) (*StorachaClient, error) {
	store, err := agentstore.NewFs(storePath)
	if err != nil {
		return nil, fmt.Errorf("creating agent store: %w", err)
	}

	// Override principal if env var is set
	if s, err := envSigner(); err != nil {
		return nil, fmt.Errorf("parsing GUPPY_PRIVATE_KEY: %w", err)
	} else if s != nil {
		if err := store.SetPrincipal(s); err != nil {
			return nil, fmt.Errorf("setting principal: %w", err)
		}
	}

	return &StorachaClient{
		connection:      MustGetConnection(),
		receiptsClient:  receiptclient.New(MustGetReceiptsURL(), receiptclient.WithHTTPClient(tracedHttpClient)),
		store:           store,
		putClient:       tracedHttpClient,
		retrievalClient: tracedHttpClient,
	}, nil
}

// DID returns the DID of the client's principal, or an empty DID if not configured.
func (c *StorachaClient) DID() did.DID {
	p, err := c.store.Principal()
	if err != nil {
		return did.DID{}
	}
	if p == nil {
		return did.DID{}
	}
	return p.DID()
}

// HasPrincipal returns true if the client has a principal (signing key) configured.
// Use this to check if you need to generate and set a new principal.
func (c *StorachaClient) HasPrincipal() (bool, error) {
	return c.store.HasPrincipal()
}

// SetPrincipal sets the principal (signing key) for the client.
// This is typically called with a newly generated signer or one loaded from elsewhere.
// The principal is persisted to the store.
func (c *StorachaClient) SetPrincipal(p principal.Signer) error {
	return c.store.SetPrincipal(p)
}

// Issuer returns the issuing signer of the client.
func (c *StorachaClient) Issuer() principal.Signer {
	p, err := c.store.Principal()
	if err != nil {
		return nil
	}
	return p
}

// Connection returns the connection used by the client.
func (c *StorachaClient) Connection() uclient.Connection {
	return c.connection
}

// Proofs returns delegations that match the given capability queries.
// It merges results from both the store and any additionally provided proofs.
func (c *StorachaClient) Proofs(queries ...agentstore.CapabilityQuery) ([]delegation.Delegation, error) {
	proofs, err := c.store.Query(queries...)
	if err != nil {
		return nil, err
	}
	if len(c.additionalProofs) > 0 {
		extra := agentstore.Query(c.additionalProofs, queries)
		proofs = append(proofs, extra...)
	}
	return proofs, nil
}

// AddProof adds an explicit delegation proof to the client.
// This is useful when the store does not contain the necessary delegations,
// for example when using a store that was set up by a different tool
// (e.g. ~/.storacha set up by upload-service).
func (c *StorachaClient) AddProof(d delegation.Delegation) {
	c.additionalProofs = append(c.additionalProofs, d)
}

// AddProofFromFile loads a delegation proof from a CAR file and adds it to
// the client. This mirrors the JS CLI's `storacha proof add <path>` command.
func (c *StorachaClient) AddProofFromFile(path string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("reading proof file: %w", err)
	}
	d, err := delegation.Extract(b)
	if err != nil {
		return fmt.Errorf("extracting proof from %s: %w", path, err)
	}
	c.additionalProofs = append(c.additionalProofs, d)
	return nil
}

// Spaces returns the list of spaces the client has access to.
func (c *StorachaClient) Spaces() ([]did.DID, error) {
	// Get all delegations
	delegations, err := c.store.Query()
	if err != nil {
		return nil, fmt.Errorf("querying delegations: %w", err)
	}

	// Extract unique space DIDs from delegations
	spaceMap := make(map[string]did.DID)
	for _, del := range delegations {
		for _, cap := range del.Capabilities() {
			with := cap.With()
			if strings.HasPrefix(with, "did:") {
				spaceDID, err := did.Parse(with)
				if err == nil {
					spaceMap[spaceDID.String()] = spaceDID
				}
			}
		}
	}

	spaces := make([]did.DID, 0, len(spaceMap))
	for _, space := range spaceMap {
		spaces = append(spaces, space)
	}

	return spaces, nil
}

// Retrieve retrieves content from a given location.
// This implements the dagservice.Retriever interface.
func (c *StorachaClient) Retrieve(ctx context.Context, location locator.Location) (io.ReadCloser, error) {
	locationCommitment := location.Commitment

	space := locationCommitment.Nb().Space

	nodeID, err := did.Parse(locationCommitment.With())
	if err != nil {
		return nil, fmt.Errorf("parsing DID of storage provider node `%s`: %w", locationCommitment.With(), err)
	}

	urls := locationCommitment.Nb().Location
	if len(urls) == 0 {
		return nil, fmt.Errorf("no URLs in location commitment")
	}
	url := urls[0] // Use first URL

	storageProvider, err := did.Parse(locationCommitment.With())
	if err != nil {
		return nil, fmt.Errorf("parsing DID of storage provider `%s`: %w", locationCommitment.With(), err)
	}

	delegations, err := c.Proofs(agentstore.CapabilityQuery{
		Can:  contentcap.Retrieve.Can(),
		With: space.String(),
	})
	if err != nil {
		return nil, err
	}
	prfs := make([]delegation.Proof, 0, len(delegations))
	// Only include the first proof to avoid exceeding header size limits
	// The first proof should contain a valid proof chain
	if len(delegations) > 0 {
		prfs = append(prfs, delegation.FromDelegation(delegations[0]))
	}

	start := location.Position.Offset
	end := start + location.Position.Length - 1

	inv, err := contentcap.Retrieve.Invoke(
		c.Issuer(),
		storageProvider,
		space.String(),
		contentcap.RetrieveCaveats{
			Blob: contentcap.BlobDigest{Digest: locationCommitment.Nb().Content.Hash()},
			Range: contentcap.Range{
				Start: start,
				End:   end,
			},
		},
		delegation.WithProof(prfs...),
	)
	if err != nil {
		return nil, fmt.Errorf("invoking `space/content/retrieve`: %w", err)
	}

	conn, err := rclient.NewConnection(nodeID, &url, rclient.WithClient(c.retrievalClient))
	if err != nil {
		return nil, fmt.Errorf("creating connection: %w", err)
	}

	_, hres, err := rclient.Execute(ctx, inv, conn, rclient.WithPublicRetrieval())
	if err != nil {
		return nil, fmt.Errorf("executing `space/content/retrieve` invocation: %w", err)
	}

	// xres will be nil for public retrieval, the actual data is in hres
	if hres == nil {
		return nil, fmt.Errorf("http response is nil")
	}

	return hres.Body(), nil
}

// SpaceBlobAdd uploads a blob to the Storacha service for a specific space.
// This is a low-level method - most users should use UploadFile or UploadDirectory instead.
//
// The method handles:
// 1. Allocating space for the blob
// 2. Uploading to the provided URL
// 3. Accepting the blob into storage
//
// Returns AddedBlob containing the location commitment for the uploaded content.
func (c *StorachaClient) SpaceBlobAdd(ctx context.Context, content io.Reader, space did.DID, precomputedDigest multihash.Multihash, size uint64, opts *UploadOptions) (*AddedBlob, error) {
	contentReader := content
	contentHash := precomputedDigest
	contentSize := size

	// Add progress tracking if provided
	if opts != nil && opts.OnProgress != nil {
		contentReader = &progressReader{
			r:        contentReader,
			total:    contentSize,
			progress: opts.OnProgress,
		}
	}

	caveats := spaceblobcap.AddCaveats{
		Blob: captypes.Blob{
			Digest: contentHash,
			Size:   contentSize,
		},
	}

	res, fx, err := invokeAndExecute[spaceblobcap.AddCaveats, spaceblobcap.AddOk](
		ctx,
		c,
		spaceblobcap.Add,
		space.String(),
		caveats,
		spaceblobcap.AddOkType(),
	)
	if err != nil {
		return nil, fmt.Errorf("invoking and executing `space/blob/add`: %w", err)
	}

	_, failErr := result.Unwrap(res)
	if failErr != nil {
		return nil, fmt.Errorf("`space/blob/add` failed: %w", failErr)
	}

	// Extract tasks from effects
	var allocateTask, putTask, acceptTask invocation.Invocation
	legacyAccept := false
	var concludeFxs []invocation.Invocation
	for _, task := range fx.Fork() {
		inv, ok := task.Invocation()
		if ok {
			switch inv.Capabilities()[0].Can() {
			case blobcap.AllocateAbility:
				allocateTask = inv
			case w3sblobcap.AllocateAbility:
				if allocateTask == nil {
					allocateTask = inv
				}
			case ucancap.ConcludeAbility:
				concludeFxs = append(concludeFxs, inv)
			case httpcap.PutAbility:
				putTask = inv
			case blobcap.AcceptAbility:
				acceptTask = inv
			case w3sblobcap.AcceptAbility:
				if acceptTask == nil {
					acceptTask = inv
					legacyAccept = true
				}
			}
		}
	}

	if allocateTask == nil || putTask == nil || acceptTask == nil || len(concludeFxs) == 0 {
		return nil, fmt.Errorf("mandatory tasks not received in space/blob/add receipt")
	}

	// Process conclude receipts
	var allocateRcpt receipt.Receipt[blobcap.AllocateOk, failure.FailureModel]
	var legacyAllocateRcpt receipt.Receipt[w3sblobcap.AllocateOk, failure.FailureModel]
	var putRcpt receipt.AnyReceipt
	var acceptRcpt receipt.Receipt[blobcap.AcceptOk, failure.FailureModel]
	var legacyAcceptRcpt receipt.Receipt[w3sblobcap.AcceptOk, failure.FailureModel]

	for _, concludeFx := range concludeFxs {
		concludeRcpt, err := getConcludeReceipt(concludeFx)
		if err != nil {
			return nil, fmt.Errorf("reading ucan/conclude receipt: %w", err)
		}

		switch concludeRcpt.Ran().Link() {
		case allocateTask.Link():
			ability := allocateTask.Capabilities()[0].Can()
			switch ability {
			case blobcap.AllocateAbility:
				allocateRcpt, err = receipt.Rebind[blobcap.AllocateOk, failure.FailureModel](concludeRcpt, blobcap.AllocateOkType(), failure.FailureType(), captypes.Converters...)
				if err != nil {
					return nil, fmt.Errorf("bad allocate receipt: %w", err)
				}
			case w3sblobcap.AllocateAbility:
				legacyAllocateRcpt, err = receipt.Rebind[w3sblobcap.AllocateOk, failure.FailureModel](concludeRcpt, w3sblobcap.AllocateOkType(), failure.FailureType(), captypes.Converters...)
				if err != nil {
					return nil, fmt.Errorf("bad legacy allocate receipt: %w", err)
				}
			}
		case putTask.Link():
			putRcpt = concludeRcpt
		case acceptTask.Link():
			ability := acceptTask.Capabilities()[0].Can()
			switch ability {
			case blobcap.AcceptAbility:
				acceptRcpt, err = receipt.Rebind[blobcap.AcceptOk, failure.FailureModel](concludeRcpt, blobcap.AcceptOkType(), failure.FailureType(), captypes.Converters...)
				if err != nil {
					return nil, fmt.Errorf("bad accept receipt: %w", err)
				}
			case w3sblobcap.AcceptAbility:
				legacyAcceptRcpt, err = receipt.Rebind[w3sblobcap.AcceptOk, failure.FailureModel](concludeRcpt, w3sblobcap.AcceptOkType(), failure.FailureType(), captypes.Converters...)
				if err != nil {
					return nil, fmt.Errorf("bad legacy accept receipt: %w", err)
				}
			}
		}
	}

	// Get upload URL and headers
	var uploadURL *url.URL
	var headers http.Header
	switch {
	case allocateRcpt != nil:
		allocateOk, err := result.Unwrap(result.MapError(allocateRcpt.Out(), failure.FromFailureModel))
		if err != nil {
			return nil, fmt.Errorf("blob allocation failed: %w", err)
		}
		if allocateOk.Address != nil {
			uploadURL = &allocateOk.Address.URL
			headers = allocateOk.Address.Headers
		}
	case legacyAllocateRcpt != nil:
		allocateOk, err := result.Unwrap(result.MapError(legacyAllocateRcpt.Out(), failure.FromFailureModel))
		if err != nil {
			return nil, fmt.Errorf("blob allocation failed: %w", err)
		}
		if allocateOk.Address != nil {
			uploadURL = &allocateOk.Address.URL
			headers = allocateOk.Address.Headers
		}
	default:
		return nil, fmt.Errorf("no allocate receipt received")
	}

	// Upload the blob
	if uploadURL != nil && headers != nil {
		if err := putBlob(ctx, c.putClient, uploadURL, headers, contentReader); err != nil {
			return nil, fmt.Errorf("putting blob: %w", err)
		}
	}

	// Send put receipt if needed
	if putRcpt == nil {
		if err := c.sendPutReceipt(ctx, putTask); err != nil {
			return nil, fmt.Errorf("sending put receipt: %w", err)
		}
	} else {
		putOk, _ := result.Unwrap(putRcpt.Out())
		if putOk == nil {
			if err := c.sendPutReceipt(ctx, putTask); err != nil {
				return nil, fmt.Errorf("sending put receipt: %w", err)
			}
		}
	}

	// Ensure blob has been accepted
	var anyAcceptRcpt receipt.AnyReceipt
	var site ucan.Link
	var pdpAcceptLink *ucan.Link
	var rcptBlocks iter.Seq2[ipld.Block, error]

	if acceptRcpt == nil && legacyAcceptRcpt == nil {
		anyAcceptRcpt, err = c.receiptsClient.Poll(ctx, acceptTask.Link(), receiptclient.WithRetries(5))
		if err != nil {
			return nil, fmt.Errorf("polling accept: %w", err)
		}
	} else if acceptRcpt != nil {
		acceptOk, failErr := result.Unwrap(result.MapError(acceptRcpt.Out(), failure.FromFailureModel))
		if failErr != nil {
			anyAcceptRcpt, err = c.receiptsClient.Poll(ctx, acceptTask.Link(), receiptclient.WithRetries(5))
			if err != nil {
				return nil, fmt.Errorf("polling accept: %w", err)
			}
		} else {
			site = acceptOk.Site
			pdpAcceptLink = acceptOk.PDP
			rcptBlocks = acceptRcpt.Blocks()
		}
	} else if legacyAcceptRcpt != nil {
		acceptOk, failErr := result.Unwrap(result.MapError(legacyAcceptRcpt.Out(), failure.FromFailureModel))
		if failErr != nil {
			anyAcceptRcpt, err = c.receiptsClient.Poll(ctx, acceptTask.Link(), receiptclient.WithRetries(5))
			if err != nil {
				return nil, fmt.Errorf("polling accept: %w", err)
			}
		} else {
			site = acceptOk.Site
			rcptBlocks = legacyAcceptRcpt.Blocks()
		}
	}

	if site == nil {
		if !legacyAccept {
			acceptRcpt, err = receipt.Rebind[blobcap.AcceptOk, failure.FailureModel](anyAcceptRcpt, blobcap.AcceptOkType(), failure.FailureType(), captypes.Converters...)
			if err != nil {
				return nil, fmt.Errorf("fetching accept receipt: %w", err)
			}
			acceptOk, err := result.Unwrap(result.MapError(acceptRcpt.Out(), failure.FromFailureModel))
			if err != nil {
				return nil, fmt.Errorf("blob/accept failed: %w", err)
			}
			site = acceptOk.Site
			pdpAcceptLink = acceptOk.PDP
			rcptBlocks = acceptRcpt.Blocks()
		} else {
			legacyAcceptRcpt, err = receipt.Rebind[w3sblobcap.AcceptOk, failure.FailureModel](anyAcceptRcpt, w3sblobcap.AcceptOkType(), failure.FailureType(), captypes.Converters...)
			if err != nil {
				return nil, fmt.Errorf("fetching legacy accept receipt: %w", err)
			}
			acceptOk, err := result.Unwrap(result.MapError(legacyAcceptRcpt.Out(), failure.FromFailureModel))
			if err != nil {
				return nil, fmt.Errorf("web3.storage/blob/accept failed: %w", err)
			}
			site = acceptOk.Site
			rcptBlocks = legacyAcceptRcpt.Blocks()
		}
	}

	blksReader, err := blockstore.NewBlockStore(blockstore.WithBlocksIterator(rcptBlocks))
	if err != nil {
		return nil, fmt.Errorf("reading location commitment blocks: %w", err)
	}

	location, err := invocation.NewInvocationView(site, blksReader)
	if err != nil {
		return nil, fmt.Errorf("creating location delegation: %w", err)
	}

	var pdpAccept invocation.Invocation
	if pdpAcceptLink != nil {
		pdpAccept, err = invocation.NewInvocationView(*pdpAcceptLink, blksReader)
		if err != nil {
			return nil, fmt.Errorf("creating `pdp/accept` delegation: %w", err)
		}
	}

	return &AddedBlob{
		Digest:    contentHash,
		Size:      contentSize,
		Location:  location,
		PDPAccept: pdpAccept,
	}, nil
}

// SpaceIndexAdd registers an index (Sharded DAG index) with the service.
// This enables efficient content discovery and retrieval.
// Called internally during the upload process.
func (c *StorachaClient) SpaceIndexAdd(ctx context.Context, indexCID cid.Cid, indexSize uint64, rootCID cid.Cid, space did.DID) error {
	indexLink := cidlink.Link{Cid: indexCID}

	inv, err := invoke[spaceindexcap.AddCaveats, spaceindexcap.AddOk](
		c,
		spaceindexcap.Add,
		space.String(),
		spaceindexcap.AddCaveats{
			Index: indexLink,
		},
	)
	if err != nil {
		return fmt.Errorf("invoking `space/index/add`: %w", err)
	}

	res, _, err := execute[spaceindexcap.AddCaveats, spaceindexcap.AddOk](
		ctx,
		c,
		spaceindexcap.Add,
		inv,
		spaceindexcap.AddOkType(),
	)
	if err != nil {
		return fmt.Errorf("executing `space/index/add`: %w", err)
	}

	_, failErr := result.Unwrap(res)
	if failErr != nil {
		return fmt.Errorf("`space/index/add` failed: %w", failErr)
	}

	return nil
}

// UploadAdd registers an upload with the Storacha service.
// This creates a mapping between the root CID and its shards.
// Called internally after successful blob uploads.
func (c *StorachaClient) UploadAdd(ctx context.Context, space did.DID, root ipld.Link, shards []ipld.Link) (uploadcap.AddOk, error) {
	res, _, err := invokeAndExecute[uploadcap.AddCaveats, uploadcap.AddOk](
		ctx,
		c,
		uploadcap.Add,
		space.String(),
		uploadcap.AddCaveats{
			Root:   root,
			Shards: shards,
		},
		uploadcap.AddOkType(),
	)
	if err != nil {
		return uploadcap.AddOk{}, fmt.Errorf("invoking and executing `upload/add`: %w", err)
	}

	addOk, failErr := result.Unwrap(res)
	if failErr != nil {
		return uploadcap.AddOk{}, fmt.Errorf("`upload/add` failed: %w", failErr)
	}

	return addOk, nil
}

// FilecoinOffer offers content to Filecoin.
func (c *StorachaClient) FilecoinOffer(ctx context.Context, space did.DID, content ipld.Link, piece ipld.Link, pdpAcceptInvocation invocation.Invocation) (filecoincap.OfferOk, error) {
	caveats := filecoincap.OfferCaveats{
		Content: content,
		Piece:   piece,
	}

	if pdpAcceptInvocation != nil {
		pdpAcceptLink := pdpAcceptInvocation.Link()
		caveats.PDP = &pdpAcceptLink
	}

	inv, err := invoke[filecoincap.OfferCaveats, filecoincap.OfferOk](
		c,
		filecoincap.Offer,
		space.String(),
		caveats,
	)
	if err != nil {
		return filecoincap.OfferOk{}, fmt.Errorf("invoking `filecoin/offer`: %w", err)
	}

	if pdpAcceptInvocation != nil {
		for b, err := range pdpAcceptInvocation.Export() {
			if err != nil {
				return filecoincap.OfferOk{}, fmt.Errorf("getting block from pdp offer invocation: %w", err)
			}
			err = inv.Attach(b)
			if err != nil {
				return filecoincap.OfferOk{}, fmt.Errorf("attaching pdp offer invocation block: %w", err)
			}
		}
	}

	res, _, err := execute[filecoincap.OfferCaveats, filecoincap.OfferOk](
		ctx,
		c,
		filecoincap.Offer,
		inv,
		filecoincap.OfferOkType(),
	)
	if err != nil {
		return filecoincap.OfferOk{}, fmt.Errorf("executing `filecoin/offer`: %w", err)
	}

	offerOk, failErr := result.Unwrap(res)
	if failErr != nil {
		return filecoincap.OfferOk{}, fmt.Errorf("`filecoin/offer` failed: %w", failErr)
	}

	return offerOk, nil
}

// Helper functions for invocation and execution

func invokeAndExecute[Caveats, Out any](
	ctx context.Context,
	c *StorachaClient,
	capParser validator.CapabilityParser[Caveats],
	with ucan.Resource,
	caveats Caveats,
	successType schema.Type,
	options ...delegation.Option,
) (result.Result[Out, resultfailure.IPLDBuilderFailure], fx.Effects, error) {
	inv, err := invoke[Caveats, Out](c, capParser, with, caveats, options...)
	if err != nil {
		return nil, nil, fmt.Errorf("invoking `%s`: %w", capParser.Can(), err)
	}
	return execute[Caveats, Out](ctx, c, capParser, inv, successType)
}

func invoke[Caveats, Out any](
	c *StorachaClient,
	capParser validator.CapabilityParser[Caveats],
	with ucan.Resource,
	caveats Caveats,
	options ...delegation.Option,
) (invocation.IssuedInvocation, error) {
	res, err := c.Proofs(agentstore.CapabilityQuery{
		Can:  capParser.Can(),
		With: with,
	})
	if err != nil {
		return nil, err
	}

	pfs := make([]delegation.Proof, 0, len(res))
	// Include all proofs to match guppy behavior
	for _, del := range res {
		pfs = append(pfs, delegation.FromDelegation(del))
	}

	inv, err := capParser.Invoke(c.Issuer(), c.Connection().ID(), with, caveats, append(options, delegation.WithProof(pfs...))...)
	if err != nil {
		return nil, err
	}

	return inv, nil
}

func execute[Caveats, Out any](
	ctx context.Context,
	c *StorachaClient,
	capParser validator.CapabilityParser[Caveats],
	inv invocation.IssuedInvocation,
	successType schema.Type,
) (result.Result[Out, resultfailure.IPLDBuilderFailure], fx.Effects, error) {
	resp, err := uclient.Execute(ctx, []invocation.Invocation{inv}, c.Connection())
	if err != nil {
		return nil, nil, fmt.Errorf("sending invocation: %w", err)
	}

	rcptlnk, ok := resp.Get(inv.Link())
	if !ok {
		return nil, nil, fmt.Errorf("receipt not found: %s", inv.Link())
	}

	reader, err := receipt.NewReceiptReaderFromTypes[Out, failure.FailureModel](successType, failure.FailureType(), captypes.Converters...)
	if err != nil {
		return nil, nil, fmt.Errorf("generating receipt reader: %w", err)
	}

	rcpt, err := reader.Read(rcptlnk, resp.Blocks())
	if err != nil {
		return nil, nil, fmt.Errorf("reading receipt: %w", err)
	}

	return result.MapError(rcpt.Out(), failure.FromFailureModel), rcpt.Fx(), nil
}

func (c *StorachaClient) sendPutReceipt(ctx context.Context, putTask invocation.Invocation) error {
	if len(putTask.Facts()) != 1 {
		return fmt.Errorf("invalid put facts, wanted 1 fact but got %d", len(putTask.Facts()))
	}

	if _, ok := putTask.Facts()[0]["keys"]; !ok {
		return fmt.Errorf("invalid put facts, missing 'keys' field")
	}

	putKeysNode, ok := putTask.Facts()[0]["keys"].(ipld.Node)
	if !ok {
		return fmt.Errorf("invalid put facts, 'keys' field is not a node")
	}

	var id did.DID
	keys := map[string][]byte{}
	it := putKeysNode.MapIterator()
	for !it.Done() {
		k, v, err := it.Next()
		if err != nil {
			return fmt.Errorf("invalid put facts: %w", err)
		}

		kStr, err := k.AsString()
		if err != nil {
			return fmt.Errorf("invalid put facts: %w", err)
		}

		switch kStr {
		case "id":
			vStr, err := v.AsString()
			if err != nil {
				return fmt.Errorf("invalid put facts: %w", err)
			}
			id, err = did.Parse(vStr)
			if err != nil {
				return fmt.Errorf("invalid put facts: %w", err)
			}
		case "keys":
			it2 := v.MapIterator()
			for !it2.Done() {
				k2, v2, err := it2.Next()
				if err != nil {
					return fmt.Errorf("invalid put facts: %w", err)
				}
				k2Str, err := k2.AsString()
				if err != nil {
					return fmt.Errorf("invalid put facts: %w", err)
				}
				v2Bytes, err := v2.AsBytes()
				if err != nil {
					return fmt.Errorf("invalid put facts: %w", err)
				}
				keys[k2Str] = v2Bytes
			}
		}
	}

	derivedKey, ok := keys[id.String()]
	if !ok {
		return fmt.Errorf("invalid put facts: missing key for %s", id.String())
	}

	derivedSigner, err := signer.Decode(derivedKey)
	if err != nil {
		return fmt.Errorf("deriving signer: %w", err)
	}

	putRcpt, err := receipt.Issue(derivedSigner, result.Ok[httpcap.PutOk, ipld.Builder](httpcap.PutOk{}), ran.FromInvocation(putTask))
	if err != nil {
		return fmt.Errorf("generating receipt: %w", err)
	}

	httpPutConcludeInvocation, err := ucancap.Conclude.Invoke(
		c.Issuer(),
		c.Connection().ID(),
		c.Issuer().DID().String(),
		ucancap.ConcludeCaveats{
			Receipt: putRcpt.Root().Link(),
		},
		delegation.WithNoExpiration(),
	)
	if err != nil {
		return fmt.Errorf("generating invocation: %w", err)
	}

	for rcptBlock, err := range putRcpt.Blocks() {
		if err != nil {
			return fmt.Errorf("getting receipt block: %w", err)
		}
		httpPutConcludeInvocation.Attach(rcptBlock)
	}

	resp, err := uclient.Execute(ctx, []invocation.Invocation{httpPutConcludeInvocation}, c.Connection())
	if err != nil {
		return fmt.Errorf("executing conclude invocation: %w", err)
	}

	rcptlnk, ok := resp.Get(httpPutConcludeInvocation.Link())
	if !ok {
		return fmt.Errorf("receipt not found: %s", httpPutConcludeInvocation.Link())
	}

	reader, err := receipt.NewReceiptReaderFromTypes[ucancap.ConcludeOk, failure.FailureModel](ucancap.ConcludeOkType(), failure.FailureType(), captypes.Converters...)
	if err != nil {
		return fmt.Errorf("generating receipt reader: %w", err)
	}

	rcpt, err := reader.Read(rcptlnk, resp.Blocks())
	if err != nil {
		return fmt.Errorf("reading receipt: %w", err)
	}

	_, err = result.Unwrap(result.MapError(rcpt.Out(), failure.FromFailureModel))
	if err != nil {
		return fmt.Errorf("ucan/conclude failed: %w", err)
	}

	return nil
}

// Helper functions

func getConcludeReceipt(concludeFx invocation.Invocation) (receipt.AnyReceipt, error) {
	concludeNb, fail := ucancap.ConcludeCaveatsReader.Read(concludeFx.Capabilities()[0].Nb())
	if fail != nil {
		return nil, fmt.Errorf("invalid conclude receipt: %w", fail)
	}

	reader := receipt.NewAnyReceiptReader(captypes.Converters...)
	rcpt, err := reader.Read(concludeNb.Receipt, concludeFx.Blocks())
	if err != nil {
		return nil, fmt.Errorf("reading receipt: %w", err)
	}

	return rcpt, nil
}

func putBlob(ctx context.Context, client *http.Client, url *url.URL, headers http.Header, body io.Reader) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, url.String(), body)
	if err != nil {
		return fmt.Errorf("creating upload request: %w", err)
	}

	// CRITICAL: AWS S3 pre-signed URLs are very sensitive to headers.
	// We must ONLY set the headers that were signed, and we must set them EXACTLY as provided.
	// Copy all header values exactly as provided - AWS S3 pre-signed URLs are very sensitive
	// to header values and order. We must preserve all values exactly as they were signed.
	// Previously we were only copying the first value (v[0]), which could cause signature
	// mismatches if headers have multiple values.
	//
	// IMPORTANT: Some headers need special handling in Go's HTTP client:
	// - Host: Must be set via req.Host, not req.Header["Host"]
	// - Content-Length: Must be set via req.ContentLength to prevent chunked encoding
	for k, v := range headers {
		// Handle special headers
		if strings.EqualFold(k, "Host") {
			if len(v) > 0 {
				req.Host = v[0]
			}
		} else if strings.EqualFold(k, "Content-Length") {
			// Parse and set Content-Length to prevent chunked transfer encoding
			if len(v) > 0 {
				if length, err := strconv.ParseInt(v[0], 10, 64); err == nil {
					req.ContentLength = length
				}
			}
			// Also set the header for completeness
			req.Header[k] = v
		} else {
			req.Header[k] = v
		}
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("uploading blob: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		bodyBytes, _ := io.ReadAll(resp.Body)
		var errorDetails string
		if len(bodyBytes) > 0 {
			bodyStr := string(bodyBytes)
			if len(bodyStr) > 500 {
				bodyStr = bodyStr[:500] + "..."
			}
			errorDetails = fmt.Sprintf(" (response body: %s)", bodyStr)
		}
		return fmt.Errorf("uploading blob: %s%s", resp.Status, errorDetails)
	}

	return nil
}

type progressReader struct {
	r         io.Reader
	total     uint64
	progress  func(uploaded int64)
	readSoFar int64
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	if n > 0 {
		p.readSoFar += int64(n)
		p.progress(p.readSoFar)
	}
	return n, err
}

// ContentPath parses a content path string and returns the CID and subpath. A
// content path can take several forms:
//
//   - /ipfs/<cid>[/<subpath>]
//   - ipfs://<cid>[/<subpath>]
//   - <cid>[/<subpath>]
//
// The subpath is returned with no leading `/`. If no subpath is specified in
// the input, an empty string is returned for it.
func ContentPath(pathStr string) (cid.Cid, string, error) {
	switch {
	case strings.HasPrefix(pathStr, "/"):
		cidAndSubpath, ok := strings.CutPrefix(pathStr, "/ipfs/")
		if !ok {
			return cid.Undef, "", fmt.Errorf("invalid path, only /ipfs/ is supported: %q", pathStr)
		}
		cidStr, subpath, _ := strings.Cut(cidAndSubpath, "/")
		pathCID, err := cid.Parse(cidStr)
		if err != nil {
			return cid.Undef, "", fmt.Errorf("parsing CID %q from IPFS path: %w", cidStr, err)
		}
		return pathCID, subpath, nil

	case strings.Contains(pathStr, "://"):
		pathURL, err := url.Parse(pathStr)
		if err != nil {
			return cid.Undef, "", fmt.Errorf("parsing URL %q: %w", pathStr, err)
		}
		if pathURL.Scheme != "ipfs" {
			return cid.Undef, "", fmt.Errorf("invalid URI, only ipfs:// is supported: %q", pathStr)
		}
		pathCID, err := cid.Parse(pathURL.Host)
		if err != nil {
			return cid.Undef, "", fmt.Errorf("parsing CID %q from IPFS URL: %w", pathURL.Host, err)
		}
		subpath, _ := strings.CutPrefix(pathURL.Path, "/")
		return pathCID, subpath, nil

	default:
		cidStr, subpath, _ := strings.Cut(pathStr, "/")
		pathCID, err := cid.Parse(cidStr)
		if err != nil {
			return cid.Undef, "", fmt.Errorf("parsing CID %q: %w", pathStr, err)
		}
		return pathCID, subpath, nil
	}
}
