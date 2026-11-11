package p2p

import (
	"bufio"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"ptp_chat/internal/identity"
	"ptp_chat/internal/wire"
)

// hello is what two peers say as their connection opens, before anything
// else is encrypted: who they are, the key of a fresh X25519 exchange, and
// their signature over it, which binds the session to the identity.
type hello struct {
	V      int    `json:"v"`
	ID     string `json:"id"`
	PK     string `json:"pk"`
	E      string `json:"e"`
	S      string `json:"s"`
	Name   string `json:"name"`
	Avatar string `json:"av,omitempty"`
	Port   int    `json:"port"`
	Addr   string `json:"addr"`
}

func (h *hello) signed() []byte { return []byte("lumen/hs/v1|" + h.E) }

// Peer is an encrypted connection to another node.
type Peer struct {
	ID       string
	Name     string
	Avatar   string
	Addr     string
	Outbound bool
	// UDP tells that the session runs over a punched path, not TCP.
	UDP      bool
	Since    time.Time
	LastSeen time.Time

	conn   net.Conn
	r      *bufio.Reader
	aead   cipher.AEAD
	wmu    sync.Mutex
	rtt    atomic.Int64
	closed atomic.Bool
}

// RTT is the last round trip measured on the connection.
func (p *Peer) RTT() time.Duration { return time.Duration(p.rtt.Load()) }

// Close ends the connection.
func (p *Peer) Close() {
	if p.closed.Swap(true) {
		return
	}
	_ = p.conn.Close()
}

// Send writes an envelope, encrypted.
func (p *Peer) Send(env *wire.Envelope) error {
	if p.closed.Load() || p.conn == nil || p.aead == nil {
		return net.ErrClosed
	}
	data, err := wire.Encode(env)
	if err != nil {
		return err
	}
	p.wmu.Lock()
	defer p.wmu.Unlock()
	_ = p.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	if err := wire.WriteFrame(p.conn, p.aead, data); err != nil {
		p.Close()
		return err
	}
	return nil
}

// sendFileFrame writes a chunk of a file: one byte says it is not JSON,
// then the transfer's id, then the sequence, then the bytes.
func (p *Peer) sendFileFrame(id string, idx int, seq int, data []byte) error {
	if p.closed.Load() {
		return net.ErrClosed
	}
	plain := make([]byte, 1+fileHeaderLen+2+4+len(data))
	plain[0] = fileFrame
	copy(plain[1:1+fileHeaderLen], fileHeader(id))
	binary.BigEndian.PutUint16(plain[1+fileHeaderLen:3+fileHeaderLen], uint16(idx))
	binary.BigEndian.PutUint32(plain[3+fileHeaderLen:7+fileHeaderLen], uint32(seq))
	copy(plain[7+fileHeaderLen:], data)

	p.wmu.Lock()
	defer p.wmu.Unlock()
	_ = p.conn.SetWriteDeadline(time.Now().Add(30 * time.Second))
	if err := wire.WriteFrame(p.conn, p.aead, plain); err != nil {
		p.Close()
		return err
	}
	return nil
}

// handshake exchanges identities over a fresh connection and returns the
// peer behind it, with the session's cipher.
func handshake(conn net.Conn, id *identity.Identity, port int, addr string) (*Peer, *hello, error) {
	_ = conn.SetDeadline(time.Now().Add(12 * time.Second))

	eph, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	ephPub := base64.StdEncoding.EncodeToString(eph.PublicKey().Bytes())
	mine := hello{
		V:      wire.Version,
		ID:     id.ID(),
		PK:     base64.StdEncoding.EncodeToString(id.Public()),
		E:      ephPub,
		S:      base64.StdEncoding.EncodeToString(ed25519.Sign(id.Key(), []byte("lumen/hs/v1|"+ephPub))),
		Name:   id.Name,
		Avatar: id.Avatar,
		Port:   port,
		Addr:   addr,
	}
	// Both ends speak first, so neither waits on the other.
	out, err := json.Marshal(&mine)
	if err != nil {
		return nil, nil, err
	}
	out = append(out, '\n')
	if _, err := conn.Write(out); err != nil {
		return nil, nil, err
	}

	r := bufio.NewReaderSize(conn, 64<<10)
	line, err := r.ReadBytes('\n')
	if err != nil {
		return nil, nil, err
	}
	var theirs hello
	if err := json.Unmarshal(line, &theirs); err != nil {
		return nil, nil, fmt.Errorf("handshake: %w", err)
	}
	if theirs.V != wire.Version {
		return nil, nil, fmt.Errorf("handshake: peer speaks version %d", theirs.V)
	}
	if theirs.ID == id.ID() {
		return nil, nil, errors.New("handshake: that is this node")
	}

	pub, err := base64.StdEncoding.DecodeString(theirs.PK)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return nil, nil, errors.New("handshake: bad public key")
	}
	if identity.ShortID(pub) != theirs.ID {
		return nil, nil, errors.New("handshake: key does not match the peer id")
	}
	sig, err := base64.StdEncoding.DecodeString(theirs.S)
	if err != nil {
		return nil, nil, errors.New("handshake: bad signature")
	}
	if !ed25519.Verify(ed25519.PublicKey(pub), theirs.signed(), sig) {
		return nil, nil, errors.New("handshake: signature does not match the ephemeral key")
	}

	remote, err := base64.StdEncoding.DecodeString(theirs.E)
	if err != nil || len(remote) != 32 {
		return nil, nil, errors.New("handshake: bad ephemeral key")
	}
	remoteKey, err := ecdh.X25519().NewPublicKey(remote)
	if err != nil {
		return nil, nil, err
	}
	shared, err := eph.ECDH(remoteKey)
	if err != nil {
		return nil, nil, err
	}
	aead, err := wire.SessionAEAD(shared, id.ID(), theirs.ID)
	if err != nil {
		return nil, nil, err
	}
	_ = conn.SetDeadline(time.Time{})

	name := theirs.Name
	if name == "" {
		name = wire.RandomName(theirs.ID)
	}
	// The peer's own listening address, not the port its connection to us
	// left from, which no one else could reach.
	listenAddr := theirs.Addr
	if listenAddr == "" {
		listenAddr = conn.RemoteAddr().String()
	}
	p := &Peer{
		ID:       theirs.ID,
		Name:     name,
		Avatar:   theirs.Avatar,
		Addr:     listenAddr,
		Since:    time.Now(),
		LastSeen: time.Now(),
		conn:     conn,
		r:        r,
		aead:     aead,
	}
	return p, &theirs, nil
}

// readLoop hands every envelope of the connection to the node.
func (n *Node) readLoop(p *Peer) {
	for {
		plain, err := wire.ReadFrame(p.r, p.aead)
		if err != nil {
			n.dropPeer(p, "connection closed")
			return
		}
		// A frame that starts with anything but '{' is file bytes.
		if len(plain) > 0 && plain[0] == fileFrame {
			p.LastSeen = time.Now()
			n.onFileFrame(p, plain)
			continue
		}
		env, err := wire.Decode(plain)
		if err != nil {
			continue
		}
		p.LastSeen = time.Now()
		n.handle(env, p)
	}
}
