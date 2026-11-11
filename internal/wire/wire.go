// Package wire defines what peers say to each other: signed envelopes,
// the session keys of a connection, and the frames they travel in.
package wire

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"time"
)

// Version is the protocol version. Peers of another version refuse each
// other's handshake.
const Version = 1

// Message kinds.
const (
	// Hello introduces a peer, once its connection is encrypted.
	Hello = "hello"
	// Chat carries a message of a room.
	Chat = "chat"
	// Typing says its sender is writing in a room.
	Typing = "typing"
	// Peers lists addresses the sender knows, so that the mesh closes.
	Peers = "peers"
	// Ping measures the round trip of a connection, and Pong answers it.
	Ping = "ping"
	Pong = "pong"
	// Bye says the sender leaves the mesh.
	Bye = "bye"
	// File carries the signalling of a transfer: offer, accept, reject,
	// done. The bytes themselves travel as binary frames.
	File = "file"
)

// MaxTTL is how many hops a relayed message may take.
const MaxTTL = 4

// Envelope is one message between peers. Relayed messages keep the
// signature of whoever wrote them, so any hop can check it.
type Envelope struct {
	V      int      `json:"v"`
	Type   string   `json:"t"`
	ID     string   `json:"id"`
	From   string   `json:"from"`
	Name   string   `json:"name,omitempty"`
	Avatar string   `json:"av,omitempty"`
	PK     string   `json:"pk,omitempty"`
	Room   string   `json:"room,omitempty"`
	Body   string   `json:"body,omitempty"`
	TS     int64    `json:"ts"`
	TTL    int      `json:"ttl,omitempty"`
	Sig    string   `json:"sig,omitempty"`
	Addr   string   `json:"addr,omitempty"`
	List   []string `json:"list,omitempty"`
	// To names the one peer a direct message is for; empty means the room,
	// which is everyone in the mesh.
	To string `json:"to,omitempty"`
}

// NewID makes an identifier for a message.
func NewID() string {
	var b [9]byte
	if _, err := rand.Read(b[:]); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return base64.RawURLEncoding.EncodeToString(b[:])
}

func (e *Envelope) canonical() []byte {
	return []byte(fmt.Sprintf("lumen/1\n%s\n%s\n%s\n%s\n%d\n%s",
		e.Type, e.ID, e.From, e.Room, e.TS, e.Body))
}

// Sign signs the envelope with the peer's long-term key.
func (e *Envelope) Sign(priv ed25519.PrivateKey, pub ed25519.PublicKey) {
	e.V = Version
	e.Sig = base64.StdEncoding.EncodeToString(ed25519.Sign(priv, e.canonical()))
	e.PK = base64.StdEncoding.EncodeToString(pub)
}

// Verify checks the signature of a message that traveled through relays.
func (e *Envelope) Verify() error {
	if e.V != Version {
		return fmt.Errorf("wire: version %d", e.V)
	}
	pub, err := base64.StdEncoding.DecodeString(e.PK)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return errors.New("wire: bad public key")
	}
	sig, err := base64.StdEncoding.DecodeString(e.Sig)
	if err != nil {
		return errors.New("wire: bad signature encoding")
	}
	if !ed25519.Verify(ed25519.PublicKey(pub), e.canonical(), sig) {
		return errors.New("wire: signature does not match")
	}
	return nil
}

// RandomName gives a peer a friendly name of its own, from where its key
// starts: two peers that meet keep theirs.
func RandomName(id string) string {
	left := []string{"蓝", "青", "琥珀", "银", "靛", "松", "雾", "珊瑚", "苔", "焰"}
	right := []string{"鲸", "鹿", "隼", "猫头鹰", "狐", "獭", "鹤", "貂", "貂", "雀"}
	if len(id) < 4 {
		return "匿名节点"
	}
	a, err := strconv.ParseUint(id[0:2], 16, 8)
	if err != nil {
		return "匿名节点"
	}
	b, err := strconv.ParseUint(id[2:4], 16, 8)
	if err != nil {
		return "匿名节点"
	}
	return left[int(a)%len(left)] + right[int(b)%len(right)]
}

// HKDF derives n bytes from a secret, as RFC 5869's HKDF-SHA256 does.
func HKDF(secret, salt []byte, info string, n int) []byte {
	extract := hmac.New(sha256.New, salt)
	extract.Write(secret)
	prk := extract.Sum(nil)

	out := make([]byte, 0, n+sha256.Size)
	var block []byte
	for i := byte(1); len(out) < n; i++ {
		h := hmac.New(sha256.New, prk)
		h.Write(block)
		h.Write([]byte(info))
		h.Write([]byte{i})
		block = h.Sum(nil)
		out = append(out, block...)
	}
	return out[:n]
}

// SessionAEAD builds the cipher of a connection from its shared secret,
// with a salt that orders the two peers the same way on both ends.
func SessionAEAD(shared []byte, idA, idB string) (cipher.AEAD, error) {
	if idA > idB {
		idA, idB = idB, idA
	}
	salt := sha256.Sum256([]byte(idA + "|" + idB))
	key := HKDF(shared, salt[:], "lumen/session/v1", 32)
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// MaxFrame bounds a frame's plaintext.
const MaxFrame = 1 << 20

// WriteFrame seals a message and writes it with its length.
func WriteFrame(w io.Writer, aead cipher.AEAD, plain []byte) error {
	if len(plain) > MaxFrame {
		return errors.New("wire: frame too large")
	}
	ns := aead.NonceSize()
	// The frame holds the length, the nonce, and what the cipher adds.
	frame := make([]byte, 4+ns, 4+ns+len(plain)+aead.Overhead())
	if _, err := rand.Read(frame[4 : 4+ns]); err != nil {
		return err
	}
	sealed := aead.Seal(frame, frame[4:4+ns], plain, nil)
	binary.BigEndian.PutUint32(sealed[:4], uint32(len(sealed)-4))
	_, err := w.Write(sealed)
	return err
}

// ReadFrame reads a sealed message and opens it.
func ReadFrame(r io.Reader, aead cipher.AEAD) ([]byte, error) {
	var head [4]byte
	if _, err := io.ReadFull(r, head[:]); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint32(head[:])
	if n == 0 || n > MaxFrame+uint32(aead.Overhead())+64 {
		return nil, errors.New("wire: bad frame length")
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, err
	}
	if len(buf) < aead.NonceSize()+aead.Overhead() {
		return nil, errors.New("wire: short frame")
	}
	nonce := buf[:aead.NonceSize()]
	return aead.Open(nil, nonce, buf[aead.NonceSize():], nil)
}

// Encode marshals an envelope.
func Encode(e *Envelope) ([]byte, error) { return json.Marshal(e) }

// Decode reads an envelope.
func Decode(b []byte) (*Envelope, error) {
	var e Envelope
	if err := json.Unmarshal(b, &e); err != nil {
		return nil, err
	}
	return &e, nil
}
