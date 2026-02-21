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

// W3AccessStore implements agentstore.Store using the JS storacha-cli JSON format.
// This enables full interoperability with the JavaScript CLI - you can login with
// Go and use the credentials with the JS CLI, or vice versa.
//
// The store reads and writes storacha-cli.json with the following special JSON format:
//   - Byte arrays are encoded as {"$bytes": [128, 38, ...]} (array of integers, not base64)
//   - Maps are encoded as {"$map": [[key, value], ...]} (array of key-value pairs)
//
// Example file location: ~/Library/Preferences/w3access/storacha-cli.json (macOS)
type W3AccessStore struct {
	mu          sync.RWMutex
	path        string
	loaded      bool
	data        *w3AccessData
	delegations []delegation.Delegation
}

var _ agentstore.Store = (*W3AccessStore)(nil)

// DefaultW3AccessStorePath returns the OS-appropriate default path for the JS CLI store.
//   - macOS: ~/Library/Preferences/w3access/
//   - Others: $XDG_CONFIG_HOME/w3access/ or ~/.config/w3access/
func DefaultW3AccessStorePath() string {
	switch runtime.GOOS {
	case "darwin":
		home, _ := os.UserHomeDir()
		return filepath.Join(home, "Library", "Preferences", "w3access")
	default:
		configDir, _ := os.UserConfigDir()
		return filepath.Join(configDir, "w3access")
	}
}

// NewW3AccessStore opens a W3AccessStore from the given path.
// The path is always treated as a directory. The store file (storacha-cli.json)
// is created inside this directory.
//
// If the directory doesn't exist, it is created with permissions 0700.
// If the store file doesn't exist, it is created with an empty structure.
//
// Example:
//
//	// Create store in custom location
//	store, err := NewW3AccessStore("./my-store")
//	// Creates: ./my-store/storacha-cli.json
//
//	// Use default JS CLI location
//	store, err := NewW3AccessStore(DefaultW3AccessStorePath())
func NewW3AccessStore(path string) (*W3AccessStore, error) {
	if err := os.MkdirAll(path, 0700); err != nil {
		return nil, fmt.Errorf("creating directory: %w", err)
	}

	storePath := filepath.Join(path, "storacha-cli.json")

	if _, err := os.Stat(storePath); os.IsNotExist(err) {
		store := &W3AccessStore{
			path:   storePath,
			loaded: true,
			data: &w3AccessData{
				Meta: w3Meta{Name: "agent", Type: "device"},
				Principal: w3Principal{
					ID:   "",
					Keys: make(map[string]w3Bytes),
				},
				Spaces:      []w3SpaceEntry{},
				Delegations: []w3DelegationEntry{},
			},
		}
		if err := store.save(); err != nil {
			return nil, err
		}
		return store, nil
	}

	return &W3AccessStore{path: storePath}, nil
}

// HasPrincipal returns true if a principal (signing key) is configured in the store.
func (s *W3AccessStore) HasPrincipal() (bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := s.ensureLoaded(); err != nil {
		return false, err
	}
	return s.data.Principal.ID != "", nil
}

// Principal returns the signing key from the store, or nil if not configured.
func (s *W3AccessStore) Principal() (principal.Signer, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := s.ensureLoaded(); err != nil {
		return nil, err
	}
	if s.data.Principal.ID == "" {
		return nil, nil
	}
	keyBytes, ok := s.data.Principal.Keys[s.data.Principal.ID]
	if !ok {
		return nil, fmt.Errorf("principal key not found")
	}
	return ed25519signer.Decode(keyBytes.Bytes)
}

// SetPrincipal stores the signing key in the store.
// The key is saved immediately to disk.
func (s *W3AccessStore) SetPrincipal(p principal.Signer) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureLoaded(); err != nil {
		return err
	}
	s.data.Principal.ID = p.DID().String()
	s.data.Principal.Keys = map[string]w3Bytes{
		p.DID().String(): {Bytes: p.Encode()},
	}
	return s.save()
}

// Delegations returns all stored delegations.
func (s *W3AccessStore) Delegations() ([]delegation.Delegation, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := s.ensureLoaded(); err != nil {
		return nil, err
	}
	return s.delegations, nil
}

// AddDelegations stores new delegations in the store.
// Each delegation is exported as CAR blocks and saved in the JS-compatible format.
// Duplicate delegations (by CID) are ignored.
func (s *W3AccessStore) AddDelegations(delegs ...delegation.Delegation) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureLoaded(); err != nil {
		return err
	}

	for _, del := range delegs {
		cidStr := del.Link().String()

		exists := false
		for _, entry := range s.data.Delegations {
			if entry.Key == cidStr {
				exists = true
				break
			}
		}
		if exists {
			continue
		}

		var blocks []w3Block
		for blk, err := range del.Export() {
			if err != nil {
				return fmt.Errorf("exporting delegation: %w", err)
			}
			blocks = append(blocks, w3Block{
				CID:   blk.Link().String(),
				Bytes: w3Bytes{Bytes: blk.Bytes()},
			})
		}

		s.data.Delegations = append(s.data.Delegations, w3DelegationEntry{
			Key: cidStr,
			Value: w3DelegationValue{
				Meta: w3DelegationMeta{
					Audience: w3AudienceMeta{
						Name: "agent",
						Type: "device",
					},
				},
				Delegation: blocks,
			},
		})
		s.delegations = append(s.delegations, del)
	}

	return s.save()
}

// Reset clears all stored data and creates a fresh empty store.
func (s *W3AccessStore) Reset() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data = &w3AccessData{
		Meta: w3Meta{Name: "agent", Type: "device"},
		Principal: w3Principal{
			ID:   "",
			Keys: make(map[string]w3Bytes),
		},
		Spaces:      []w3SpaceEntry{},
		Delegations: []w3DelegationEntry{},
	}
	s.delegations = nil
	s.loaded = true
	return s.save()
}

// Query returns delegations matching the given capability queries.
// If no queries are provided, returns all delegations.
func (s *W3AccessStore) Query(queries ...agentstore.CapabilityQuery) ([]delegation.Delegation, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := s.ensureLoaded(); err != nil {
		return nil, err
	}
	return agentstore.Query(s.delegations, queries), nil
}

// ensureLoaded lazily loads the store data from disk on first access.
func (s *W3AccessStore) ensureLoaded() error {
	if s.loaded {
		return nil
	}

	data, err := os.ReadFile(s.path)
	if err != nil {
		return fmt.Errorf("reading w3access store: %w", err)
	}

	s.data = &w3AccessData{}
	if err := s.data.UnmarshalJSON(data); err != nil {
		return fmt.Errorf("parsing w3access store: %w", err)
	}

	for _, entry := range s.data.Delegations {
		del, err := decodeDelegation(entry.Key, entry.Value.Delegation)
		if err != nil {
			continue
		}
		s.delegations = append(s.delegations, del)
	}

	s.loaded = true
	return nil
}

// save writes the store data to disk in JS-compatible JSON format.
func (s *W3AccessStore) save() error {
	data, err := s.data.MarshalJSON()
	if err != nil {
		return fmt.Errorf("marshaling w3access store: %w", err)
	}

	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("creating w3access directory: %w", err)
	}

	return os.WriteFile(s.path, data, 0600)
}

// --- Data types matching JS storacha-cli JSON format ---

// w3AccessData represents the root structure of storacha-cli.json.
type w3AccessData struct {
	Meta         w3Meta
	Principal    w3Principal
	Spaces       []w3SpaceEntry
	Delegations  []w3DelegationEntry
	CurrentSpace string
}

// MarshalJSON produces JS-compatible JSON with $map structures for spaces and delegations.
func (d *w3AccessData) MarshalJSON() ([]byte, error) {
	type intermediate struct {
		Meta         w3Meta                 `json:"meta"`
		Principal    w3Principal            `json:"principal"`
		Spaces       map[string]interface{} `json:"spaces,omitempty"`
		Delegations  map[string]interface{} `json:"delegations,omitempty"`
		CurrentSpace string                 `json:"currentSpace,omitempty"`
	}

	spacesMap := map[string]interface{}{
		"$map": make([][2]interface{}, len(d.Spaces)),
	}
	for i, s := range d.Spaces {
		spacesMap["$map"].([][2]interface{})[i] = [2]interface{}{s.DID, s.Value}
	}

	delegationsMap := map[string]interface{}{
		"$map": make([][2]interface{}, len(d.Delegations)),
	}
	for i, d := range d.Delegations {
		delegationsMap["$map"].([][2]interface{})[i] = [2]interface{}{d.Key, d.Value}
	}

	return json.Marshal(&intermediate{
		Meta:         d.Meta,
		Principal:    d.Principal,
		Spaces:       spacesMap,
		Delegations:  delegationsMap,
		CurrentSpace: d.CurrentSpace,
	})
}

// UnmarshalJSON parses JS-compatible JSON with $map structures.
func (d *w3AccessData) UnmarshalJSON(data []byte) error {
	type raw struct {
		Meta         w3Meta                     `json:"meta"`
		Principal    w3Principal                `json:"principal"`
		Spaces       map[string]json.RawMessage `json:"spaces"`
		Delegations  map[string]json.RawMessage `json:"delegations"`
		CurrentSpace string                     `json:"currentSpace"`
	}

	var r raw
	if err := json.Unmarshal(data, &r); err != nil {
		return err
	}

	d.Meta = r.Meta
	d.Principal = r.Principal
	d.CurrentSpace = r.CurrentSpace

	if r.Spaces != nil {
		if mapRaw, ok := r.Spaces["$map"]; ok {
			var entries []w3SpaceEntry
			if err := json.Unmarshal(mapRaw, &entries); err == nil {
				d.Spaces = entries
			}
		}
	}

	if r.Delegations != nil {
		if mapRaw, ok := r.Delegations["$map"]; ok {
			var rawEntries [][2]json.RawMessage
			if err := json.Unmarshal(mapRaw, &rawEntries); err == nil {
				for _, pair := range rawEntries {
					var key string
					var value w3DelegationValue
					if err := json.Unmarshal(pair[0], &key); err != nil {
						continue
					}
					if err := json.Unmarshal(pair[1], &value); err != nil {
						continue
					}
					d.Delegations = append(d.Delegations, w3DelegationEntry{Key: key, Value: value})
				}
			}
		}
	}

	return nil
}

// w3Meta contains metadata about the agent.
type w3Meta struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// w3Principal contains the agent's DID and signing key(s).
type w3Principal struct {
	ID   string             `json:"id"`
	Keys map[string]w3Bytes `json:"keys"`
}

// w3Bytes represents a byte array in JS-compatible format.
// In JSON, it serializes as {"$bytes": [128, 38, ...]} instead of base64.
type w3Bytes struct {
	Bytes []byte
}

// MarshalJSON encodes bytes as an array of integers with $bytes wrapper.
// This matches the JS CLI format exactly.
func (b w3Bytes) MarshalJSON() ([]byte, error) {
	byteArray := make([]int, len(b.Bytes))
	for i, v := range b.Bytes {
		byteArray[i] = int(v)
	}
	return json.Marshal(map[string]interface{}{"$bytes": byteArray})
}

// UnmarshalJSON decodes the $bytes array format back to bytes.
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

// w3SpaceEntry represents a space in the $map array.
type w3SpaceEntry struct {
	DID   string
	Value map[string]interface{}
}

// UnmarshalJSON parses a [did, value] pair from the $map array.
func (e *w3SpaceEntry) UnmarshalJSON(data []byte) error {
	var pair [2]json.RawMessage
	if err := json.Unmarshal(data, &pair); err != nil {
		return err
	}
	if err := json.Unmarshal(pair[0], &e.DID); err != nil {
		return err
	}
	return json.Unmarshal(pair[1], &e.Value)
}

// w3DelegationEntry represents a delegation in the $map array.
type w3DelegationEntry struct {
	Key   string
	Value w3DelegationValue
}

// w3DelegationValue contains delegation metadata and CAR blocks.
type w3DelegationValue struct {
	Meta       w3DelegationMeta `json:"meta"`
	Delegation []w3Block        `json:"delegation"`
}

// w3DelegationMeta contains metadata about the delegation's audience.
type w3DelegationMeta struct {
	Audience w3AudienceMeta `json:"audience"`
}

// w3AudienceMeta identifies the agent that received the delegation.
type w3AudienceMeta struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// w3Block represents a single IPLD block in a delegation's CAR file.
type w3Block struct {
	CID   string  `json:"cid"`
	Bytes w3Bytes `json:"bytes"`
}

// decodeDelegation rebuilds a delegation.Delegation from stored CAR blocks.
func decodeDelegation(rootCIDStr string, blocks []w3Block) (delegation.Delegation, error) {
	rootCID, err := cid.Decode(rootCIDStr)
	if err != nil {
		return nil, fmt.Errorf("decoding root CID: %w", err)
	}
	rootLink := cidlink.Link{Cid: rootCID}

	ipldBlocks := make([]ucanipld.Block, 0, len(blocks))
	for _, b := range blocks {
		c, err := cid.Decode(b.CID)
		if err != nil {
			return nil, fmt.Errorf("decoding block CID: %w", err)
		}
		lnk := cidlink.Link{Cid: c}
		ipldBlocks = append(ipldBlocks, block.NewBlock(lnk, b.Bytes.Bytes))
	}

	bs, err := blockstore.NewBlockStore(blockstore.WithBlocks(ipldBlocks))
	if err != nil {
		return nil, fmt.Errorf("building blockstore: %w", err)
	}

	return delegation.NewDelegationView(rootLink, bs)
}

// NewStorachaClientFromW3Access creates a StorachaClient using the JS CLI-compatible store.
// This is the recommended way to create a client when you want interoperability
// with the JavaScript storacha-cli.
//
// If path is empty, uses the default JS CLI location.
//
// Example:
//
//	// Use default location (share with JS CLI)
//	client, err := NewStorachaClientFromW3Access("")
//
//	// Use custom location
//	client, err := NewStorachaClientFromW3Access("./my-store")
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
