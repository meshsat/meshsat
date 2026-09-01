package oob

import (
	"crypto/ecdh"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"meshsat/internal/database"
	"meshsat/internal/keystore"
)

// Key sources on a peer row.
const (
	KeySourceBundle = "bundle"
	KeySourceECDH   = "ecdh"
)

// PeerSpec is the operator-facing shape for creating or updating a peer.
type PeerSpec struct {
	Alias     string            `json:"alias"`
	Role      string            `json:"role"`                 // readonly | control
	Source    string            `json:"source,omitempty"`     // random (default) | ecdh
	SignerID  string            `json:"signer_id,omitempty"`  // trusted_peers.signer_id, required for ecdh
	Addresses map[string]string `json:"addresses,omitempty"`  // interface id -> bearer address
	EncPolicy map[string]bool   `json:"enc_policy,omitempty"` // interface id -> encrypt requests (absent = true)
}

// ECDHMaterial is what the ECDH provisioning path needs for one peer: this
// kit's X25519 private key, the peer's X25519 public key, and both
// destination hashes. main.go supplies the resolver from the routing
// identity, trusted_peers and the destination table.
type ECDHMaterial struct {
	MyPriv   *ecdh.PrivateKey
	PeerPub  *ecdh.PublicKey
	MyHash   []byte
	PeerHash []byte
}

func normalizeRole(role string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "", RoleReadonly:
		return RoleReadonly, nil
	case RoleControl:
		return RoleControl, nil
	}
	return "", fmt.Errorf("oob: role must be %s or %s", RoleReadonly, RoleControl)
}

func marshalMap[T any](m map[string]T) string {
	if len(m) == 0 {
		return "{}"
	}
	b, err := json.Marshal(m)
	if err != nil {
		return "{}"
	}
	return string(b)
}

// CreatePeer provisions a new peer on the random-key or ECDH path.
func (s *Service) CreatePeer(spec PeerSpec) (*database.OOBPeer, error) {
	alias := strings.TrimSpace(spec.Alias)
	if alias == "" {
		return nil, errors.New("oob: alias required")
	}
	if _, err := s.d.DB.GetOOBPeerByAlias(alias); err == nil {
		return nil, fmt.Errorf("oob: peer %q already exists", alias)
	}
	role, err := normalizeRole(spec.Role)
	if err != nil {
		return nil, err
	}
	if s.d.Keys == nil {
		return nil, errors.New("oob: keystore not available")
	}

	var (
		key       []byte
		localRole Role
		source    string
	)
	switch strings.ToLower(spec.Source) {
	case "", "random", KeySourceBundle:
		source = KeySourceBundle
		localRole = RoleIssuer
		// Regenerate on the (unlikely) wire id collision.
		for attempt := 0; attempt < 8; attempt++ {
			k, err := RandomKey()
			if err != nil {
				return nil, err
			}
			if _, err := s.d.DB.GetOOBPeer(PeerIDFromKey(k)); errors.Is(err, database.ErrOOBPeerNotFound) {
				key = k
				break
			}
		}
		if key == nil {
			return nil, errors.New("oob: could not allocate a peer id")
		}
	case KeySourceECDH:
		if s.d.ECDH == nil {
			return nil, errors.New("oob: ecdh provisioning not available")
		}
		if spec.SignerID == "" {
			return nil, errors.New("oob: signer_id required for ecdh")
		}
		m, err := s.d.ECDH(spec.SignerID)
		if err != nil {
			return nil, err
		}
		k, err := DeriveECDHKey(m.MyPriv, m.PeerPub, m.MyHash, m.PeerHash)
		if err != nil {
			return nil, err
		}
		if _, err := s.d.DB.GetOOBPeer(PeerIDFromKey(k)); err == nil {
			return nil, errors.New("oob: peer id collision on the ecdh path, pair with a bundle instead")
		}
		key = k
		localRole = RoleForECDH(m.MyHash, m.PeerHash)
		source = KeySourceECDH
	default:
		return nil, fmt.Errorf("oob: unknown key source %q", spec.Source)
	}

	if _, err := s.d.Keys.StoreKey("mgmt", alias, key); err != nil {
		return nil, fmt.Errorf("oob: store key: %w", err)
	}
	p := &database.OOBPeer{
		PeerID:    PeerIDFromKey(key),
		Alias:     alias,
		SignerID:  spec.SignerID,
		KeyRef:    "mgmt:" + alias,
		KeySource: source,
		LocalRole: int(localRole),
		Role:      role,
		Enabled:   true,
		Addresses: marshalMap(spec.Addresses),
		EncPolicy: marshalMap(spec.EncPolicy),
	}
	if err := s.d.DB.InsertOOBPeer(p); err != nil {
		_ = s.d.Keys.RevokeKey("mgmt", alias)
		return nil, err
	}
	return s.d.DB.GetOOBPeer(p.PeerID)
}

// UpdatePeer changes the operator-editable fields. A nil enabled keeps the
// current value. The alias (and so the key reference) is immutable.
func (s *Service) UpdatePeer(id uint16, spec PeerSpec, enabled *bool) (*database.OOBPeer, error) {
	p, err := s.d.DB.GetOOBPeer(id)
	if err != nil {
		return nil, err
	}
	if spec.Role != "" {
		role, err := normalizeRole(spec.Role)
		if err != nil {
			return nil, err
		}
		p.Role = role
	}
	if spec.SignerID != "" {
		p.SignerID = spec.SignerID
	}
	if spec.Addresses != nil {
		p.Addresses = marshalMap(spec.Addresses)
	}
	if spec.EncPolicy != nil {
		p.EncPolicy = marshalMap(spec.EncPolicy)
	}
	if enabled != nil {
		p.Enabled = *enabled
	}
	if err := s.d.DB.UpdateOOBPeer(p); err != nil {
		return nil, err
	}
	return s.d.DB.GetOOBPeer(id)
}

// DeletePeer revokes the key, cancels any revert timer and removes the row.
func (s *Service) DeletePeer(id uint16) error {
	p, err := s.d.DB.GetOOBPeer(id)
	if err != nil {
		return err
	}
	if s.d.Keys != nil {
		_ = s.d.Keys.RevokeKey("mgmt", p.Alias)
	}
	s.cancelReverts()
	return s.d.DB.DeleteOOBPeer(id)
}

// ListPeers returns every peer.
func (s *Service) ListPeers() ([]database.OOBPeer, error) {
	return s.d.DB.ListOOBPeers()
}

// IssueBundle wraps the peer's key in a signed QR bundle under the issuer's
// alias. Only bundle-sourced keys can be issued; an ECDH key never leaves
// the kit.
func (s *Service) IssueBundle(id uint16, issuerAlias string) (url string, err error) {
	p, err := s.d.DB.GetOOBPeer(id)
	if err != nil {
		return "", err
	}
	if p.KeySource != KeySourceBundle {
		return "", errors.New("oob: an ecdh key is derived on both sides and is never exported")
	}
	if s.d.Keys == nil {
		return "", errors.New("oob: keystore not available")
	}
	raw, _, err := s.d.Keys.GetKey("mgmt", p.Alias)
	if err != nil {
		return "", fmt.Errorf("oob: key: %w", err)
	}
	if len(raw) != KeyLen {
		return "", errors.New("oob: stored key has the wrong length")
	}
	issuerAlias = strings.TrimSpace(issuerAlias)
	if issuerAlias == "" {
		issuerAlias = s.d.LocalAlias
	}
	if issuerAlias == "" {
		return "", errors.New("oob: issuer alias required")
	}
	var key [KeyLen]byte
	copy(key[:], raw)
	_, url, err = s.d.Keys.CreateBundleFromEntries([]keystore.BundleEntry{{
		ChannelType: keystore.ChannelMgmt,
		Address:     issuerAlias,
		Key:         key,
	}})
	return url, err
}

// RegisterImportedPeer is called by the key import path when a bundle
// carries a mgmt entry: the key is already stored as mgmt:<issuer alias>;
// this side becomes the importer, readonly until promoted.
func (s *Service) RegisterImportedPeer(issuerAlias string, rawKey []byte) (*database.OOBPeer, error) {
	issuerAlias = strings.TrimSpace(issuerAlias)
	if issuerAlias == "" {
		return nil, errors.New("oob: bundle entry has no issuer alias")
	}
	if len(rawKey) != KeyLen {
		return nil, ErrBadKey
	}
	id := PeerIDFromKey(rawKey)
	if existing, err := s.d.DB.GetOOBPeer(id); err == nil {
		return existing, nil
	}
	if existing, err := s.d.DB.GetOOBPeerByAlias(issuerAlias); err == nil {
		return existing, nil
	}
	p := &database.OOBPeer{
		PeerID:    id,
		Alias:     issuerAlias,
		KeyRef:    "mgmt:" + issuerAlias,
		KeySource: KeySourceBundle,
		LocalRole: int(RoleImporter),
		Role:      RoleReadonly,
		Enabled:   true,
	}
	if err := s.d.DB.InsertOOBPeer(p); err != nil {
		return nil, err
	}
	return s.d.DB.GetOOBPeer(id)
}

// resolveAddress returns the peer's address on a bearer, or "" when the
// bearer needs none (Iridium, MQTT) or none is configured.
func resolveAddress(p *database.OOBPeer, ifaceID string) string {
	var m map[string]string
	if err := json.Unmarshal([]byte(p.Addresses), &m); err != nil {
		return ""
	}
	return m[ifaceID]
}

// wantsEnc reports whether requests this kit originates to the peer on
// the bearer should be encrypted. Absent means yes.
func wantsEnc(p *database.OOBPeer, ifaceID string) bool {
	var m map[string]bool
	if err := json.Unmarshal([]byte(p.EncPolicy), &m); err != nil {
		return true
	}
	if v, ok := m[ifaceID]; ok {
		return v
	}
	return true
}

// peerKey fetches the raw management key for a peer.
func (s *Service) peerKey(p *database.OOBPeer) ([]byte, error) {
	if s.d.Keys == nil {
		return nil, errors.New("oob: keystore not available")
	}
	raw, _, err := s.d.Keys.GetKey("mgmt", p.Alias)
	if err != nil {
		return nil, err
	}
	if len(raw) != KeyLen {
		return nil, ErrBadKey
	}
	return raw, nil
}
