// Package identity keeps the node's long-term Ed25519 key pair, which names
// the peer and signs every message it sends.
package identity

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

// Identity is a peer's name and key pair.
type Identity struct {
	Name string `json:"name"`
	// Avatar is the look this node chose, as "colour|glyph"; empty means
	// one derived from the key.
	Avatar string `json:"avatar,omitempty"`
	Priv   []byte `json:"priv"`
	Pub    []byte `json:"pub"`
}

// New makes an identity with a fresh key pair.
func New(name string) (*Identity, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	return &Identity{Name: name, Priv: priv, Pub: pub}, nil
}

// Key returns the private key.
func (i *Identity) Key() ed25519.PrivateKey { return ed25519.PrivateKey(i.Priv) }

// Public returns the public key.
func (i *Identity) Public() ed25519.PublicKey { return ed25519.PublicKey(i.Pub) }

// ID is the peer's identifier, derived from its public key.
func (i *Identity) ID() string { return ShortID(i.Public()) }

// ShortID derives a peer's identifier from its public key: the first ten
// bytes of its SHA-256, in hex.
func ShortID(pub ed25519.PublicKey) string {
	sum := sha256.Sum256(pub)
	return hex.EncodeToString(sum[:10])
}

// Fingerprint groups an identifier in blocks of four, as a key's
// fingerprint is written.
func Fingerprint(id string) string {
	out := make([]byte, 0, len(id)+len(id)/4)
	for i := 0; i < len(id); i += 4 {
		if i > 0 {
			out = append(out, ' ')
		}
		end := min(i+4, len(id))
		out = append(out, id[i:end]...)
	}
	return string(out)
}

// Load reads the identity in dir, or makes one with the given name.
func Load(dir, name string) (*Identity, error) {
	path := filepath.Join(dir, "identity.json")
	data, err := os.ReadFile(path)
	if err == nil {
		var id Identity
		if err := json.Unmarshal(data, &id); err != nil {
			return nil, err
		}
		if len(id.Priv) != ed25519.PrivateKeySize || len(id.Pub) != ed25519.PublicKeySize {
			return nil, errors.New("identity: malformed key")
		}
		if id.Name == "" {
			id.Name = name
		}
		return &id, nil
	}
	if !os.IsNotExist(err) {
		return nil, err
	}
	id, err := New(name)
	if err != nil {
		return nil, err
	}
	if err := id.Save(dir); err != nil {
		return nil, err
	}
	return id, nil
}

// Save writes the identity to dir, readable by its owner alone.
func (i *Identity) Save(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(i, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(dir, "identity.json")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
