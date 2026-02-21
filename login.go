// Package go_storacha_upload_client_kit provides a Go client for uploading content to Storacha.
// It implements a complete login flow compatible with the JavaScript storacha-cli,
// allowing seamless interoperability between Go and JS tools.
package go_storacha_upload_client_kit

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"iter"
	"net/url"
	"strings"
	"time"

	ipldcar "github.com/ipld/go-car"
	"github.com/ipld/go-ipld-prime/datamodel"
	cidlink "github.com/ipld/go-ipld-prime/linking/cid"
	"github.com/ipld/go-ipld-prime/node/basicnode"
	accesscap "github.com/storacha/go-libstoracha/capabilities/access"
	"github.com/storacha/go-libstoracha/capabilities/types"
	"github.com/storacha/go-libstoracha/failure"
	uclient "github.com/storacha/go-ucanto/client"
	"github.com/storacha/go-ucanto/core/dag/blockstore"
	"github.com/storacha/go-ucanto/core/delegation"
	"github.com/storacha/go-ucanto/core/invocation"
	"github.com/storacha/go-ucanto/core/ipld"
	"github.com/storacha/go-ucanto/core/ipld/block"
	"github.com/storacha/go-ucanto/core/receipt"
	"github.com/storacha/go-ucanto/core/result"
	"github.com/storacha/go-ucanto/did"
	"github.com/storacha/go-ucanto/principal"
	"github.com/storacha/go-ucanto/ucan"
)

const (
	loginTimeout      = 15 * time.Minute
	loginPollInterval = 1 * time.Second
)

// LoginOptions configures the login flow behavior.
type LoginOptions struct {
	// AppName identifies your application in the confirmation email.
	// Optional - if empty, no app name is included.
	AppName string
}

// LoginResult contains the outcome of a successful login.
type LoginResult struct {
	// AccountDID is the DID of the authenticated account (e.g., did:mailto:domain:user).
	AccountDID did.DID
	// Delegations are the capability delegations granted by the account.
	Delegations []delegation.Delegation
}

// emailToDidMailto converts an email address to a did:mailto DID.
// The format is: did:mailto:domain:urlencoded-local-part
// e.g., "user@example.com" → "did:mailto:example.com:user"
func emailToDidMailto(email string) (did.DID, error) {
	parts := strings.Split(email, "@")
	if len(parts) != 2 {
		return did.DID{}, fmt.Errorf("invalid email address: %s", email)
	}
	didStr := fmt.Sprintf("did:mailto:%s:%s", parts[1], url.QueryEscape(parts[0]))
	return did.Parse(didStr)
}

// Login initiates the email-based authentication flow.
// It sends a confirmation email to the provided address and waits for the user
// to click the confirmation link. The returned LoginResult contains the account
// DID and any delegations granted during login.
//
// The client must have a principal (signing key) configured before calling Login.
// Use HasPrincipal() to check, and SetPrincipal() or GenerateSigner() to set one.
//
// Example:
//
//	result, err := client.Login(ctx, "user@example.com", nil)
//	if err != nil {
//	    log.Fatal(err)
//	}
//	fmt.Printf("Logged in as: %s\n", result.AccountDID)
func (c *StorachaClient) Login(ctx context.Context, email string, opts *LoginOptions) (*LoginResult, error) {
	accountDID, err := emailToDidMailto(email)
	if err != nil {
		return nil, fmt.Errorf("parsing email: %w", err)
	}

	if c.Issuer() == nil {
		return nil, fmt.Errorf("no principal configured - cannot login")
	}

	result, err := c.requestAccess(ctx, accountDID, opts)
	if err != nil {
		return nil, fmt.Errorf("requesting access: %w", err)
	}

	return result, nil
}

// requestAccess sends an access/authorize request to the Storacha service.
// This triggers an email to be sent to the account for confirmation.
// It then polls for the delegations to be claimed after confirmation.
func (c *StorachaClient) requestAccess(ctx context.Context, accountDID did.DID, opts *LoginOptions) (*LoginResult, error) {
	audienceDID, err := did.Parse(c.Connection().ID().DID().String())
	if err != nil {
		return nil, fmt.Errorf("parsing audience DID: %w", err)
	}

	authorizeInv, err := createAuthorizeInvocation(c.Issuer(), audienceDID, accountDID, opts)
	if err != nil {
		return nil, fmt.Errorf("creating authorize invocation: %w", err)
	}

	resp, err := uclient.Execute(ctx, []invocation.Invocation{authorizeInv}, c.Connection())
	if err != nil {
		return nil, fmt.Errorf("executing access/authorize: %w", err)
	}

	rcptLnk, ok := resp.Get(authorizeInv.Link())
	if !ok {
		return nil, fmt.Errorf("receipt not found for access/authorize")
	}

	reader, err := receipt.NewReceiptReaderFromTypes[accesscap.AuthorizeOk, failure.FailureModel](
		accesscap.AuthorizeOkType(), failure.FailureType(), types.Converters...)
	if err != nil {
		return nil, fmt.Errorf("creating receipt reader: %w", err)
	}

	rcpt, err := reader.Read(rcptLnk, resp.Blocks())
	if err != nil {
		return nil, fmt.Errorf("reading authorize receipt: %w", err)
	}

	authorizeOk, failErr := result.Unwrap(result.MapError(rcpt.Out(), failure.FromFailureModel))
	if failErr != nil {
		return nil, fmt.Errorf("access/authorize failed: %w", failErr)
	}

	requestLink := authorizeOk.Request
	expiration := authorizeOk.Expiration

	delegations, err := c.pollClaim(ctx, c.Issuer().DID(), requestLink, expiration)
	if err != nil {
		return nil, fmt.Errorf("polling for delegations: %w", err)
	}

	return &LoginResult{
		AccountDID:  accountDID,
		Delegations: delegations,
	}, nil
}

// simpleFact implements ucan.FactBuilder for adding facts to delegations.
// Facts are key-value pairs that provide context about the delegation.
type simpleFact struct {
	key   string
	value string
}

// ToIPLD converts the fact to its IPLD representation.
func (f *simpleFact) ToIPLD() (map[string]datamodel.Node, error) {
	np := basicnode.Prototype.Any
	nb := np.NewBuilder()
	ma, _ := nb.BeginMap(1)
	ma.AssembleKey().AssignString(f.key)
	ma.AssembleValue().AssignString(f.value)
	ma.Finish()
	return map[string]datamodel.Node{f.key: nb.Build()}, nil
}

// createAuthorizeInvocation builds an access/authorize invocation.
// This is the first step in the login flow, requesting authorization
// for the agent to act on behalf of the account.
func createAuthorizeInvocation(issuer principal.Signer, audience did.DID, accountDID did.DID, opts *LoginOptions) (invocation.Invocation, error) {
	accountDIDStr := accountDID.String()

	caveats := accesscap.AuthorizeCaveats{
		Iss: &accountDIDStr,
		Att: []accesscap.CapabilityRequest{
			{Can: "*"},
		},
	}

	var delegationOpts []delegation.Option
	if opts != nil && opts.AppName != "" {
		fact := &simpleFact{key: "appName", value: opts.AppName}
		delegationOpts = append(delegationOpts, delegation.WithFacts([]ucan.FactBuilder{fact}))
	}

	inv, err := accesscap.Authorize.Invoke(
		issuer,
		audience,
		issuer.DID().String(),
		caveats,
		delegationOpts...,
	)
	if err != nil {
		return nil, fmt.Errorf("creating invocation: %w", err)
	}

	return inv, nil
}

// pollClaim repeatedly checks for delegations to be available after the user
// confirms the login via email. It polls until the expiration time or until
// context is cancelled.
func (c *StorachaClient) pollClaim(ctx context.Context, agentDID did.DID, requestLink ucan.Link, expiration ucan.UTCUnixTimestamp) ([]delegation.Delegation, error) {
	timeout := time.Unix(int64(expiration), 0)
	ticker := time.NewTicker(loginPollInterval)
	defer ticker.Stop()

	pollCount := 0
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
			pollCount++
			if time.Now().After(timeout) {
				return nil, fmt.Errorf("login request expired at %s", timeout.Format(time.RFC3339))
			}

			delegations, err := c.claimDelegations(ctx, agentDID, requestLink)
			if err != nil {
				continue
			}
			if len(delegations) > 0 {
				return delegations, nil
			}
			fmt.Printf("\rWaiting for email confirmation... (attempt %d)", pollCount)
		}
	}
}

// claimDelegations calls access/claim to retrieve delegations granted to the agent.
// Returns an empty slice if no delegations are available yet.
func (c *StorachaClient) claimDelegations(ctx context.Context, agentDID did.DID, requestLink ucan.Link) ([]delegation.Delegation, error) {
	audienceDID, err := did.Parse(c.Connection().ID().DID().String())
	if err != nil {
		return nil, fmt.Errorf("parsing audience DID: %w", err)
	}

	claimInv, err := accesscap.Claim.Invoke(
		c.Issuer(),
		audienceDID,
		agentDID.String(),
		accesscap.ClaimCaveats{},
	)
	if err != nil {
		return nil, fmt.Errorf("creating claim invocation: %w", err)
	}

	resp, err := uclient.Execute(ctx, []invocation.Invocation{claimInv}, c.Connection())
	if err != nil {
		return nil, fmt.Errorf("executing access/claim: %w", err)
	}

	rcptLnk, ok := resp.Get(claimInv.Link())
	if !ok {
		return nil, fmt.Errorf("receipt not found for access/claim")
	}

	reader, err := receipt.NewReceiptReaderFromTypes[accesscap.ClaimOk, failure.FailureModel](
		accesscap.ClaimOkType(), failure.FailureType(), types.Converters...)
	if err != nil {
		return nil, fmt.Errorf("creating receipt reader: %w", err)
	}

	rcpt, err := reader.Read(rcptLnk, resp.Blocks())
	if err != nil {
		return nil, fmt.Errorf("reading claim receipt: %w", err)
	}

	claimOk, failErr := result.Unwrap(result.MapError(rcpt.Out(), failure.FromFailureModel))
	if failErr != nil {
		return nil, fmt.Errorf("access/claim failed: %w", failErr)
	}

	delegations, err := decodeDelegationsFromClaim(claimOk.Delegations, resp.Blocks(), requestLink)
	if err != nil {
		return nil, fmt.Errorf("decoding delegations: %w", err)
	}

	return delegations, nil
}

// decodeDelegationsFromClaim extracts delegations from the claim response.
// It filters to only include delegations that match the requested access.
func decodeDelegationsFromClaim(delModel accesscap.DelegationsModel, blocks iter.Seq2[ipld.Block, error], requestLink ucan.Link) ([]delegation.Delegation, error) {
	var delegations []delegation.Delegation

	for _, key := range delModel.Keys {
		bytes, ok := delModel.Values[key]
		if !ok {
			continue
		}

		del, err := decodeCarDelegation(bytes)
		if err != nil {
			continue
		}

		if isRequestedAccess(del, requestLink) {
			delegations = append(delegations, del)
		}
	}

	return delegations, nil
}

// decodeCarDelegation parses a CAR-encoded delegation.
// The access/claim endpoint returns delegations as CAR files,
// not as delegation.Extract() format, so we need to use a CAR reader.
func decodeCarDelegation(carBytes []byte) (delegation.Delegation, error) {
	reader, err := ipldcar.NewCarReader(bytes.NewReader(carBytes))
	if err != nil {
		return nil, fmt.Errorf("creating CAR reader: %w", err)
	}

	if len(reader.Header.Roots) == 0 {
		return nil, fmt.Errorf("CAR has no roots")
	}

	rootCID := reader.Header.Roots[0]
	rootLink := cidlink.Link{Cid: rootCID}

	var ipldBlocks []ipld.Block
	for {
		blk, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("reading CAR block: %w", err)
		}
		lnk := cidlink.Link{Cid: blk.Cid()}
		ipldBlocks = append(ipldBlocks, block.NewBlock(lnk, blk.RawData()))
	}

	bs, err := blockstore.NewBlockStore(blockstore.WithBlocks(ipldBlocks))
	if err != nil {
		return nil, fmt.Errorf("building blockstore: %w", err)
	}

	del, err := delegation.NewDelegationView(rootLink, bs)
	if err != nil {
		return nil, fmt.Errorf("building delegation view: %w", err)
	}

	return del, nil
}

// isRequestedAccess checks if a delegation contains the "access/request" fact
// matching the given request link. This identifies delegations granted
// as part of the login flow.
func isRequestedAccess(del delegation.Delegation, requestLink ucan.Link) bool {
	for _, fact := range del.Facts() {
		if v, ok := fact[accesscap.AuthorizeRequestFactKey]; ok {
			var linkStr string
			switch val := v.(type) {
			case string:
				linkStr = val
			case ucan.Link:
				linkStr = val.String()
			case ipld.Node:
				if val.Kind() == datamodel.Kind_Link {
					lnk, err := val.AsLink()
					if err == nil {
						linkStr = lnk.String()
					}
				} else if s, err := val.AsString(); err == nil {
					linkStr = s
				}
			}
			if linkStr == requestLink.String() {
				return true
			}
		}
	}
	return false
}

// SaveDelegations stores delegations in the client's store.
// This persists the granted capabilities for future use.
func (c *StorachaClient) SaveDelegations(delegations ...delegation.Delegation) error {
	if err := c.store.AddDelegations(delegations...); err != nil {
		return fmt.Errorf("adding delegations to store: %w", err)
	}
	return nil
}

// LoginAndSave performs the complete login flow and saves the resulting delegations.
// This is the recommended way to handle login as it ensures credentials are persisted.
//
// Example:
//
//	result, err := client.LoginAndSave(ctx, "user@example.com", nil)
//	if err != nil {
//	    log.Fatal(err)
//	}
//	fmt.Printf("Logged in as: %s\n", result.AccountDID)
func (c *StorachaClient) LoginAndSave(ctx context.Context, email string, opts *LoginOptions) (*LoginResult, error) {
	result, err := c.Login(ctx, email, opts)
	if err != nil {
		return nil, err
	}

	if err := c.SaveDelegations(result.Delegations...); err != nil {
		return nil, fmt.Errorf("saving delegations: %w", err)
	}

	return result, nil
}
