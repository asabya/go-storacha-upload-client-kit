package go_storacha_upload_client_kit

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"

	"github.com/storacha/go-libstoracha/digestutil"
	"github.com/storacha/go-ucanto/core/invocation"
	"github.com/storacha/go-ucanto/core/message"
	"github.com/storacha/go-ucanto/did"
	hcmsg "github.com/storacha/go-ucanto/transport/headercar/message"
	"github.com/storacha/go-ucanto/ucan"
	"github.com/storacha/guppy/pkg/client/locator"
	"github.com/storacha/indexing-service/pkg/service/queryresult"
	"github.com/storacha/indexing-service/pkg/types"
)

const (
	defaultIndexerHost = "indexer.storacha.network"
	claimsPath         = "/claims"
)

// HTTPIndexerClient is an HTTP-based client for the indexing service that
// implements the locator.IndexerClient interface.
type HTTPIndexerClient struct {
	serviceURL *url.URL
	httpClient *http.Client
	principal  ucan.Principal
}

var _ locator.IndexerClient = (*HTTPIndexerClient)(nil)

// NewHTTPIndexerClient creates a new HTTP-based indexer client.
// If serviceURL is nil, it uses the default indexing service URL.
// If httpClient is nil, it uses http.DefaultClient.
func NewHTTPIndexerClient(principal ucan.Principal, serviceURL *url.URL, httpClient *http.Client) (*HTTPIndexerClient, error) {
	if serviceURL == nil {
		defaultURL, err := url.Parse(fmt.Sprintf("https://%s", defaultIndexerHost))
		if err != nil {
			return nil, fmt.Errorf("parsing default indexer URL: %w", err)
		}
		serviceURL = defaultURL
	}

	if httpClient == nil {
		httpClient = http.DefaultClient
	}

	return &HTTPIndexerClient{
		serviceURL: serviceURL,
		httpClient: httpClient,
		principal:  principal,
	}, nil
}

// QueryClaims queries the indexing service for claims matching the given query.
// This implements the locator.IndexerClient interface.
func (c *HTTPIndexerClient) QueryClaims(ctx context.Context, query types.Query) (types.QueryResult, error) {
	// Build the query URL
	queryURL := c.serviceURL.JoinPath(claimsPath)
	q := queryURL.Query()

	// Add query type
	q.Add("type", query.Type.String())

	// Add multihashes to query parameters using digestutil.Format
	for _, hash := range query.Hashes {
		q.Add("multihash", digestutil.Format(hash))
	}

	// Add spaces to query parameters
	for _, space := range query.Match.Subject {
		q.Add("spaces", space.String())
	}

	queryURL.RawQuery = q.Encode()
	// Create HTTP request
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, queryURL.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}

	// Add delegations as X-Agent-Message header if present
	if len(query.Delegations) > 0 {
		invs := make([]invocation.Invocation, 0, len(query.Delegations))
		for _, d := range query.Delegations {
			invs = append(invs, d)
		}
		msg, err := message.Build(invs, nil)
		if err != nil {
			return nil, fmt.Errorf("building agent message: %w", err)
		}
		headerValue, err := hcmsg.EncodeHeader(msg)
		if err != nil {
			return nil, fmt.Errorf("encoding %s header: %w", hcmsg.HeaderName, err)
		}
		req.Header.Set(hcmsg.HeaderName, headerValue)
	}

	// Execute request
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("executing request: %w", err)
	}
	defer resp.Body.Close()

	// Check response status
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("unexpected status: %d, message: %s", resp.StatusCode, string(body))
	}

	// Parse the response as a QueryResult using the existing queryresult.Extract
	return queryresult.Extract(resp.Body)
}

// MustGetHTTPIndexClient creates a new HTTP-based indexer client using
// environment variables for configuration.
func MustGetHTTPIndexClient() (*HTTPIndexerClient, ucan.Principal) {
	indexerURLStr := GetEnv("STORACHA_INDEXING_SERVICE_URL", fmt.Sprintf("https://%s", defaultIndexerHost))

	indexerURL, err := url.Parse(indexerURLStr)
	if err != nil {
		panic(fmt.Sprintf("parsing indexer URL: %v", err))
	}

	indexerDIDStr := GetEnv("STORACHA_INDEXING_SERVICE_DID", defaultIndexerDID)

	indexerPrincipal, err := did.Parse(indexerDIDStr)
	if err != nil {
		panic(fmt.Sprintf("parsing indexer DID: %v", err))
	}

	// Use the traced HTTP client if available, otherwise use default
	var httpClient *http.Client
	if tracedHttpClient != nil {
		httpClient = tracedHttpClient
	} else {
		httpClient = http.DefaultClient
	}

	client, err := NewHTTPIndexerClient(indexerPrincipal, indexerURL, httpClient)
	if err != nil {
		panic(fmt.Sprintf("creating HTTP indexer client: %v", err))
	}

	return client, indexerPrincipal
}

// GetEnv gets an environment variable with a default value
func GetEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

// DefaultStorePath returns the default guppy agent store path (~/.storacha/guppy).
// This is the store populated by guppy's space creation and provisioning flow.
//
// If your store was set up by a different tool (e.g. the JS upload-service CLI
// at ~/.storacha), use that path instead and pass the required space delegation
// via client.AddProofFromFile().
func DefaultStorePath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("getting home directory: %w", err)
	}
	return fmt.Sprintf("%s/.storacha/guppy", home), nil
}
