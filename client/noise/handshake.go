// Package noise implements the Tailscale 2021 control protocol Noise IK handshake.
//
// Based on the Tailscale open source codebase (BSD-3-Clause).
// Self-contained to avoid the massive tailscale.com dependency tree.
package noise

import (
	"context"
	"crypto/cipher"
	"encoding/binary"
	"errors"
	"fmt"
	"hash"
	"io"
	"net"
	"strconv"
	"time"

	"golang.org/x/crypto/blake2s"
	chp "golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/curve25519"
	"golang.org/x/crypto/hkdf"
)

const (
	protocolName                = "Noise_IK_25519_ChaChaPoly_BLAKE2s"
	protocolVersionPrefix       = "Tailscale Control Protocol v"
	msgTypeInitiation       byte = 1
	msgTypeResponse         byte = 2
	msgTypeError            byte = 3
	msgTypeRecord           byte = 4
	headerLen                   = 3
	initiationHeaderLen         = 5

	maxMessageSize    = 4096
	maxCiphertextSize = maxMessageSize - 3
	maxPlaintextSize  = maxCiphertextSize - chp.Overhead
)

// Key is a 32-byte Curve25519 key.
type Key [32]byte

// NewKey generates a new random key.
func NewKey() Key {
	var k Key
	randRead(k[:])
	k[0] &= 248
	k[31] &= 127
	k[31] |= 64
	return k
}

// Public returns the public key for this private key.
func (k Key) Public() Key {
	var pub [32]byte
	curve25519.ScalarBaseMult(&pub, (*[32]byte)(&k))
	return Key(pub)
}

// IsZero reports whether k is the zero value.
func (k Key) IsZero() bool {
	for _, b := range k {
		if b != 0 {
			return false
		}
	}
	return true
}

// Bytes returns the raw bytes of the key.
func (k Key) Bytes() []byte {
	return k[:]
}

// KeyFromBytes creates a key from raw bytes.
func KeyFromBytes(b []byte) Key {
	var k Key
	copy(k[:], b)
	return k
}

// --- Wire Protocol Messages ---

// initiationMessage is the protocol message sent from client to server.
//
// 2b: protocol version
// 1b: message type (0x01)
// 2b: payload length (96)
// 32b: client ephemeral public key (cleartext)
// 48b: client machine public key (encrypted)
// 16b: message tag
type initiationMessage [101]byte

func mkInitiationMessage(protocolVersion uint16) initiationMessage {
	var ret initiationMessage
	binary.BigEndian.PutUint16(ret[:2], protocolVersion)
	ret[2] = msgTypeInitiation
	binary.BigEndian.PutUint16(ret[3:5], uint16(len(ret.Payload())))
	return ret
}

func (m *initiationMessage) Header() []byte  { return m[:initiationHeaderLen] }
func (m *initiationMessage) Payload() []byte { return m[initiationHeaderLen:] }
func (m *initiationMessage) EphemeralPub() []byte {
	return m[initiationHeaderLen : initiationHeaderLen+32]
}
func (m *initiationMessage) MachinePub() []byte {
	return m[initiationHeaderLen+32 : initiationHeaderLen+32+48]
}
func (m *initiationMessage) Tag() []byte { return m[initiationHeaderLen+32+48:] }

// responseMessage is the protocol message sent from server to client.
//
// 1b: message type (0x02)
// 2b: payload length (48)
// 32b: control ephemeral public key (cleartext)
// 16b: message tag
type responseMessage [51]byte

func mkResponseMessage() responseMessage {
	var ret responseMessage
	ret[0] = msgTypeResponse
	binary.BigEndian.PutUint16(ret[1:], uint16(len(ret.Payload())))
	return ret
}

func (m *responseMessage) Header() []byte  { return m[:headerLen] }
func (m *responseMessage) Payload() []byte { return m[headerLen:] }
func (m *responseMessage) EphemeralPub() []byte { return m[headerLen : headerLen+32] }
func (m *responseMessage) Tag() []byte          { return m[headerLen+32:] }

func (m *responseMessage) Type() byte  { return m[0] }
func (m *responseMessage) Length() int { return int(binary.BigEndian.Uint16(m[1:3])) }

// --- Noise Handshake ---

type symmetricState struct {
	finished bool
	h        [blake2s.Size]byte
	ck       [blake2s.Size]byte
}

func (s *symmetricState) checkFinished() {
	if s.finished {
		panic("attempted to use symmetricState after Split was called")
	}
}

func (s *symmetricState) Initialize() {
	s.checkFinished()
	s.h = blake2s.Sum256([]byte(protocolName))
	s.ck = s.h
}

func (s *symmetricState) MixHash(data []byte) {
	s.checkFinished()
	h := newBLAKE2s()
	h.Write(s.h[:])
	h.Write(data)
	h.Sum(s.h[:0])
}

func (s *symmetricState) MixDH(priv Key, pub Key) (*singleUseCHP, error) {
	s.checkFinished()
	keyData, err := curve25519.X25519(priv[:], pub[:])
	if err != nil {
		return nil, fmt.Errorf("computing X25519: %w", err)
	}
	r := hkdf.New(newBLAKE2s, keyData, s.ck[:], nil)
	if _, err := io.ReadFull(r, s.ck[:]); err != nil {
		return nil, fmt.Errorf("extracting ck: %w", err)
	}
	var k [chp.KeySize]byte
	if _, err := io.ReadFull(r, k[:]); err != nil {
		return nil, fmt.Errorf("extracting k: %w", err)
	}
	return newSingleUseCHP(k), nil
}

func (s *symmetricState) EncryptAndHash(cipher *singleUseCHP, ciphertext, plaintext []byte) {
	s.checkFinished()
	if len(ciphertext) != len(plaintext)+chp.Overhead {
		panic("ciphertext is wrong size for given plaintext")
	}
	ret := cipher.Seal(ciphertext[:0], plaintext, s.h[:])
	s.MixHash(ret)
}

func (s *symmetricState) DecryptAndHash(cipher *singleUseCHP, plaintext, ciphertext []byte) error {
	s.checkFinished()
	if len(ciphertext) != len(plaintext)+chp.Overhead {
		return errors.New("plaintext is wrong size for given ciphertext")
	}
	if _, err := cipher.Open(plaintext[:0], ciphertext, s.h[:]); err != nil {
		return err
	}
	s.MixHash(ciphertext)
	return nil
}

func (s *symmetricState) Split() (c1, c2 cipher.AEAD, err error) {
	s.finished = true
	var k1, k2 [chp.KeySize]byte
	r := hkdf.New(newBLAKE2s, nil, s.ck[:], nil)
	if _, err := io.ReadFull(r, k1[:]); err != nil {
		return nil, nil, fmt.Errorf("extracting k1: %w", err)
	}
	if _, err := io.ReadFull(r, k2[:]); err != nil {
		return nil, nil, fmt.Errorf("extracting k2: %w", err)
	}
	c1, err = chp.New(k1[:])
	if err != nil {
		return nil, nil, fmt.Errorf("constructing AEAD c1: %w", err)
	}
	c2, err = chp.New(k2[:])
	if err != nil {
		return nil, nil, fmt.Errorf("constructing AEAD c2: %w", err)
	}
	return c1, c2, nil
}

func newBLAKE2s() hash.Hash {
	h, err := blake2s.New256(nil)
	if err != nil {
		panic(err)
	}
	return h
}

type singleUseCHP struct {
	c cipher.AEAD
}

func newSingleUseCHP(key [chp.KeySize]byte) *singleUseCHP {
	aead, err := chp.New(key[:])
	if err != nil {
		panic(err)
	}
	return &singleUseCHP{c: aead}
}

func (c *singleUseCHP) Seal(dst, plaintext, additionalData []byte) []byte {
	if c.c == nil {
		panic("Attempted reuse of singleUseAEAD")
	}
	cipher := c.c
	c.c = nil
	var nonce [chp.NonceSize]byte
	return cipher.Seal(dst, nonce[:], plaintext, additionalData)
}

func (c *singleUseCHP) Open(dst, ciphertext, additionalData []byte) ([]byte, error) {
	if c.c == nil {
		panic("Attempted reuse of singleUseAEAD")
	}
	cipher := c.c
	c.c = nil
	var nonce [chp.NonceSize]byte
	return cipher.Open(dst, nonce[:], ciphertext, additionalData)
}

func protocolVersionPrologue(version uint16) []byte {
	ret := make([]byte, 0, len(protocolVersionPrefix)+5)
	ret = append(ret, protocolVersionPrefix...)
	return strconv.AppendUint(ret, uint64(version), 10)
}

// --- Client Handshake ---

// HandshakeResult holds the final state after a Noise IK handshake.
type HandshakeResult struct {
	Conn             net.Conn
	Version          uint16
	Peer             Key
	HandshakeHash    [blake2s.Size]byte
	TX, RX           cipher.AEAD
	ServerNoiseKey   Key
	ClientEphemeral  Key
}

// ClientDeferred initiates a control client handshake.
// Returns the initial message to send to the server and a continuation function.
func ClientDeferred(machineKey Key, controlKey Key, protocolVersion uint16) (initialHandshake []byte, continueHandshake func(context.Context, net.Conn) (*HandshakeResult, error), err error) {
	var s symmetricState
	s.Initialize()

	s.MixHash(protocolVersionPrologue(protocolVersion))

	s.MixHash(controlKey[:])

	init := mkInitiationMessage(protocolVersion)
	machineEphemeral := NewKey()
	machineEphemeralPub := machineEphemeral.Public()
	copy(init.EphemeralPub(), machineEphemeralPub[:])
	s.MixHash(machineEphemeralPub[:])
	cipher, err := s.MixDH(machineEphemeral, controlKey)
	if err != nil {
		return nil, nil, fmt.Errorf("computing es: %w", err)
	}
	machinePub := machineKey.Public()
	s.EncryptAndHash(cipher, init.MachinePub(), machinePub[:])
	cipher, err = s.MixDH(machineKey, controlKey)
	if err != nil {
		return nil, nil, fmt.Errorf("computing ss: %w", err)
	}
	s.EncryptAndHash(cipher, init.Tag(), nil)

	saveS := s
	saveMachineKey := machineKey
	saveMachineEphemeral := machineEphemeral
	saveControlKey := controlKey

	cont := func(ctx context.Context, conn net.Conn) (*HandshakeResult, error) {
		return continueClientHandshake(ctx, conn, &saveS, saveMachineKey, saveMachineEphemeral, saveControlKey, protocolVersion)
	}
	return init[:], cont, nil
}

func continueClientHandshake(ctx context.Context, conn net.Conn, s *symmetricState,
	machineKey, machineEphemeral Key, controlKey Key, protocolVersion uint16) (*HandshakeResult, error) {

	defer func() { s.finished = true }()

	if deadline, ok := ctx.Deadline(); ok {
		if err := conn.SetDeadline(deadline); err != nil {
			return nil, fmt.Errorf("setting conn deadline: %w", err)
		}
		defer conn.SetDeadline(time.Time{})
	}

	var resp responseMessage
	if _, err := io.ReadFull(conn, resp.Header()); err != nil {
		return nil, fmt.Errorf("reading response header: %w", err)
	}
	if resp.Type() != msgTypeResponse {
		if resp.Type() != msgTypeError {
			return nil, fmt.Errorf("unexpected response message type %d", resp.Type())
		}
		msg := make([]byte, resp.Length())
		if _, err := io.ReadFull(conn, msg); err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("server error: %q", msg)
	}
	if resp.Length() != len(resp.Payload()) {
		return nil, fmt.Errorf("wrong length %d received for handshake response", resp.Length())
	}
	if _, err := io.ReadFull(conn, resp.Payload()); err != nil {
		return nil, err
	}

	controlEphemeralPub := KeyFromBytes(resp.EphemeralPub())
	s.MixHash(controlEphemeralPub[:])
	if _, err := s.MixDH(machineEphemeral, controlEphemeralPub); err != nil {
		return nil, fmt.Errorf("computing ee: %w", err)
	}
	cipher, err := s.MixDH(machineKey, controlEphemeralPub)
	if err != nil {
		return nil, fmt.Errorf("computing se: %w", err)
	}
	if err := s.DecryptAndHash(cipher, nil, resp.Tag()); err != nil {
		return nil, fmt.Errorf("decrypting payload: %w", err)
	}

	c1, c2, err := s.Split()
	if err != nil {
		return nil, fmt.Errorf("finalizing handshake: %w", err)
	}

	return &HandshakeResult{
		Conn:          conn,
		Version:       protocolVersion,
		Peer:          controlKey,
		HandshakeHash: s.h,
		TX:            c1,
		RX:            c2,
	}, nil
}

// Client synchronously upgrades a net.Conn to a noise connection.
func Client(ctx context.Context, conn net.Conn, machineKey Key, controlKey Key, protocolVersion uint16) (*HandshakeResult, error) {
	init, cont, err := ClientDeferred(machineKey, controlKey, protocolVersion)
	if err != nil {
		return nil, err
	}
	if _, err := conn.Write(init); err != nil {
		return nil, err
	}
	return cont(ctx, conn)
}