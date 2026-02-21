package go_storacha_upload_client_kit

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"

	"github.com/ipfs/go-cid"
	cidlink "github.com/ipld/go-ipld-prime/linking/cid"
	"github.com/storacha/go-ucanto/core/dag/blockstore"
	"github.com/storacha/go-ucanto/core/delegation"
	ucanipld "github.com/storacha/go-ucanto/core/ipld"
	"github.com/storacha/go-ucanto/core/ipld/block"
	"github.com/storacha/go-ucanto/principal"
	ed25519signer "github.com/storacha/go-ucanto/principal/ed25519/signer"
	"github.com/storacha/guppy/pkg/agentstore"
	receiptclient "github.com/storacha/guppy/pkg/receipt"
)

// W3AccessStore implements agentstore.Store by reading the JS CLI's agent data
// from ~/Library/Preferences/w3access/storacha-cli.json (macOS) or
// ~/.config/w3access/storacha-cli.json (Linux).
//
// It is read-only with respect to the on-disk file: SetPrincipal and
// AddDelegations are kept in memory for the lifetime of the store and are
// never written back to disk.
type W3AccessStore struct {
	mu               sync.RWMutex
	path             string
	principal        principal.Signer       // in-memory override
	extraDelegations []delegation.Delegation // in-memory additions
}

var _ agentstore.Store = (*W3AccessStore)(nil)

// DefaultW3AccessStorePath returns the OS-appropriate path to the storacha-cli.json
// written by the JS storacha CLI.
func DefaultW3AccessStorePath() string {
	switch runtime.GOOS {
	case "darwin":
		home, _ := os.UserHomeDir()
		return filepath.Join(home, "Library", "Preferences", "w3access", "storacha-cli.json")
	default:
		configDir, _ := os.UserConfigDir()
		return filepath.Join(configDir, "w3access", "storacha-cli.json")
	}
}

// NewW3AccessStore opens a W3AccessStore from the given JSON file path.
func NewW3AccessStore(path string) (*W3AccessStore, error) {
	if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("w3access store file not found at %s: %w", path, err)
	}
	return &W3AccessStore{path: path}, nil
}

func (s *W3AccessStore) HasPrincipal() (bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.principal != nil {
		return true, nil
	}
	p, err := s.loadPrincipal()
	if err != nil {
		return false, err
	}
	return p != nil, nil
}

func (s *W3AccessStore) Principal() (principal.Signer, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.principal != nil {
		return s.principal, nil
	}
	return s.loadPrincipal()
}

// SetPrincipal overrides the principal in memory only. The file is not modified.
func (s *W3AccessStore) SetPrincipal(p principal.Signer) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.principal = p
	return nil
}

func (s *W3AccessStore) Delegations() ([]delegation.Delegation, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.loadAllDelegations()
}

// AddDelegations appends delegations in memory only. The file is not modified.
func (s *W3AccessStore) AddDelegations(delegs ...delegation.Delegation) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.extraDelegations = append(s.extraDelegations, delegs...)
	return nil
}

// Reset clears the in-memory additions. The file is not modified.
func (s *W3AccessStore) Reset() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.principal = nil
	s.extraDelegations = nil
	return nil
}

func (s *W3AccessStore) Query(queries ...agentstore.CapabilityQuery) ([]delegation.Delegation, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	all, err := s.loadAllDelegations()
	if err != nil {
		return nil, err
	}
	return agentstore.Query(all, queries), nil
}

// loadPrincipal reads and decodes the principal from the JSON file.
// Must be called with at least a read lock held.
func (s *W3AccessStore) loadPrincipal() (principal.Signer, error) {
	raw, err := parseW3AccessFile(s.path)
	if err != nil {
		return nil, err
	}

	// The principal is stored as { id: "did:key:...", keys: { "did:key:...": {$bytes:[...]} } }
	keyBytes, ok := raw.Principal.Keys[raw.Principal.ID]
	if !ok {
		return nil, fmt.Errorf("w3access: principal key for %s not found", raw.Principal.ID)
	}

	signer, err := ed25519signer.Decode(keyBytes.Bytes)
	if err != nil {
		return nil, fmt.Errorf("w3access: decoding principal key: %w", err)
	}
	return signer, nil
}

// loadAllDelegations reads delegations from the file and merges in-memory additions.
// Must be called with at least a read lock held.
func (s *W3AccessStore) loadAllDelegations() ([]delegation.Delegation, error) {
	raw, err := parseW3AccessFile(s.path)
	if err != nil {
		return nil, err
	}

	delegations, err := decodeDelegations(raw.Delegations)
	if err != nil {
		return nil, err
	}

	return append(delegations, s.extraDelegations...), nil
}

// --- JSON data model ---

type w3AccessFile struct {
	Principal   w3Principal `json:"principal"`
	Delegations []w3MapEntry
}

type w3Principal struct {
	ID   string
	Keys map[string]w3Bytes
}

// w3Bytes deserialises { "$bytes": [0,1,2,...] }.
type w3Bytes struct {
	Bytes []byte
}

func (b *w3Bytes) UnmarshalJSON(data []byte) error {
	var raw struct {
		Bytes []int `json:"$bytes"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	b.Bytes = make([]byte, len(raw.Bytes))
	for i, v := range raw.Bytes {
		b.Bytes[i] = byte(v)
	}
	return nil
}

// w3MapEntry is one [[key, value],...] pair from a "$map" encoded JS Map.
type w3MapEntry struct {
	Key   string
	Value w3DelegationValue
}

type w3DelegationValue struct {
	Delegation []w3Block `json:"delegation"`
}

type w3Block struct {
	CID   string  `json:"cid"`
	Bytes w3Bytes `json:"bytes"`
}

// parseW3AccessFile reads and partially parses the storacha-cli.json file.
func parseW3AccessFile(path string) (*w3AccessFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading w3access store %s: %w", path, err)
	}

	// Top-level object: only the fields we care about.
	var top struct {
		Principal struct {
			ID   string                    `json:"id"`
			Keys map[string]w3Bytes        `json:"keys"`
		} `json:"principal"`
		Delegations struct {
			Map []json.RawMessage `json:"$map"`
		} `json:"delegations"`
	}
	if err := json.Unmarshal(data, &top); err != nil {
		return nil, fmt.Errorf("parsing w3access store: %w", err)
	}

	result := &w3AccessFile{
		Principal: w3Principal{
			ID:   top.Principal.ID,
			Keys: top.Principal.Keys,
		},
	}

	// Decode each delegation map entry: [ "cidString", { delegation: [{cid, bytes},...] } ]
	for i, rawEntry := range top.Delegations.Map {
		// Each entry is a 2-element JSON array [string, object]
		var pair [2]json.RawMessage
		if err := json.Unmarshal(rawEntry, &pair); err != nil {
			return nil, fmt.Errorf("w3access delegation entry %d: %w", i, err)
		}
		var key string
		if err := json.Unmarshal(pair[0], &key); err != nil {
			return nil, fmt.Errorf("w3access delegation key %d: %w", i, err)
		}
		var val w3DelegationValue
		if err := json.Unmarshal(pair[1], &val); err != nil {
			return nil, fmt.Errorf("w3access delegation value %d: %w", i, err)
		}
		result.Delegations = append(result.Delegations, w3MapEntry{Key: key, Value: val})
	}

	return result, nil
}

// decodeDelegations reconstructs delegation.Delegation values from parsed blocks.
func decodeDelegations(entries []w3MapEntry) ([]delegation.Delegation, error) {
	result := make([]delegation.Delegation, 0, len(entries))
	for _, entry := range entries {
		del, err := decodeDelegation(entry.Key, entry.Value.Delegation)
		if err != nil {
			// Skip undecodable delegations (e.g. ucan/attest session proofs
			// whose issuer key is a transient service key we cannot verify).
			// They are included as raw blocks inside the authorizing delegation,
			// so the UCAN validator will still find them.
			continue
		}
		result = append(result, del)
	}
	return result, nil
}

// decodeDelegation builds a delegation.Delegation from the root CID string and
// the list of IPLD blocks that make up the delegation DAG.
func decodeDelegation(rootCIDStr string, blocks []w3Block) (delegation.Delegation, error) {
	rootCID, err := cid.Decode(rootCIDStr)
	if err != nil {
		return nil, fmt.Errorf("decoding root CID %s: %w", rootCIDStr, err)
	}
	rootLink := cidlink.Link{Cid: rootCID}

	ipldBlocks := make([]ucanipld.Block, 0, len(blocks))
	for _, b := range blocks {
		c, err := cid.Decode(b.CID)
		if err != nil {
			return nil, fmt.Errorf("decoding block CID %s: %w", b.CID, err)
		}
		lnk := cidlink.Link{Cid: c}
		ipldBlocks = append(ipldBlocks, block.NewBlock(lnk, b.Bytes.Bytes))
	}

	bs, err := blockstore.NewBlockStore(blockstore.WithBlocks(ipldBlocks))
	if err != nil {
		return nil, fmt.Errorf("building blockstore: %w", err)
	}

	del, err := delegation.NewDelegationView(rootLink, bs)
	if err != nil {
		return nil, fmt.Errorf("building delegation: %w", err)
	}
	return del, nil
}

// NewStorachaClientFromW3Access creates a StorachaClient that reads agent
// identity and delegations from the JS CLI's storacha-cli.json file.
//
// If path is empty, DefaultW3AccessStorePath() is used.
func NewStorachaClientFromW3Access(path string) (*StorachaClient, error) {
	if path == "" {
		path = DefaultW3AccessStorePath()
	}
	store, err := NewW3AccessStore(path)
	if err != nil {
		return nil, fmt.Errorf("opening w3access store: %w", err)
	}

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
