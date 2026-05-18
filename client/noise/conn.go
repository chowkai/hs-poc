package noise

import (
	"crypto/cipher"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"sync"
	"time"
)

// randRead fills b with random bytes.
func randRead(b []byte) {
	if _, err := io.ReadFull(rand.Reader, b); err != nil {
		panic(fmt.Sprintf("crypto/rand failed: %v", err))
	}
}

// Conn is a noise-secured connection. It implements net.Conn.
type Conn struct {
	conn net.Conn
	rx   rxState
	tx   txState
}

type rxState struct {
	sync.Mutex
	cipher    cipher.AEAD
	nonce     nonce
	plaintext []byte
}

type txState struct {
	sync.Mutex
	cipher cipher.AEAD
	nonce  nonce
}

type nonce struct {
	val uint64
}

// Valid reports whether the nonce can still be incremented safely.
func (n *nonce) Valid() bool {
	return n.val != invalidNonce
}

// Increment increments the nonce. Must only be called on valid nonces.
func (n *nonce) Increment() {
	n.val++
}

const invalidNonce = ^uint64(0)

// NewConn creates a noise-secured connection wrapping an underlying net.Conn
// using the provided AEAD ciphers for TX and RX directions.
func NewConn(conn net.Conn, tx, rx cipher.AEAD) *Conn {
	return &Conn{
		conn: conn,
		tx:   txState{cipher: tx},
		rx:   rxState{cipher: rx},
	}
}

func (c *Conn) Read(b []byte) (int, error) {
	c.rx.Lock()
	defer c.rx.Unlock()

	if c.rx.cipher == nil {
		return 0, net.ErrClosed
	}

	for len(c.rx.plaintext) == 0 {
		if err := c.decryptOneLocked(); err != nil {
			return 0, err
		}
	}
	n := copy(b, c.rx.plaintext)
	c.rx.plaintext = c.rx.plaintext[n:]
	if len(c.rx.plaintext) == 0 {
		c.rx.plaintext = nil
	}
	return n, nil
}

func (c *Conn) decryptOneLocked() error {
	c.rx.plaintext = nil

	var hdr [headerLen]byte
	if _, err := io.ReadFull(c.conn, hdr[:]); err != nil {
		return err
	}

	if hdr[0] != msgTypeRecord {
		// Read and discard error payload
		errLen := int(binary.BigEndian.Uint16(hdr[1:3]))
		io.CopyN(io.Discard, c.conn, int64(errLen))
		return fmt.Errorf("unexpected message type: %d", hdr[0])
	}

	pktLen := int(binary.BigEndian.Uint16(hdr[1:3]))
	buf := make([]byte, pktLen)
	if _, err := io.ReadFull(c.conn, buf); err != nil {
		return err
	}

	if !c.rx.nonce.Valid() {
		return fmt.Errorf("cipher exhausted")
	}

	var err error
	c.rx.plaintext, err = c.rx.cipher.Open(buf[:0], nonceBytes(c.rx.nonce.val), buf, nil)
	c.rx.nonce.Increment()
	if err != nil {
		c.rx.cipher = nil
		return err
	}
	return nil
}

func (c *Conn) Write(b []byte) (int, error) {
	c.tx.Lock()
	defer c.tx.Unlock()

	if c.tx.cipher == nil {
		return 0, net.ErrClosed
	}

	if !c.tx.nonce.Valid() {
		return 0, fmt.Errorf("cipher exhausted")
	}

	// We write data in chunks of maxPlaintextSize
	totalWritten := 0
	for len(b) > 0 {
		chunk := b
		if len(chunk) > maxPlaintextSize {
			chunk = b[:maxPlaintextSize]
		}

		ctLen := len(chunk) + chpOverhead()
		frame := make([]byte, headerLen+ctLen)
		frame[0] = msgTypeRecord
		binary.BigEndian.PutUint16(frame[1:3], uint16(ctLen))

		sealed := c.tx.cipher.Seal(frame[headerLen:headerLen], nonceBytes(c.tx.nonce.val), chunk, nil)
		frame = frame[:headerLen+len(sealed)]
		c.tx.nonce.Increment()

		if _, err := c.conn.Write(frame); err != nil {
			return totalWritten, err
		}
		totalWritten += len(chunk)
		b = b[len(chunk):]
	}
	return totalWritten, nil
}

func (c *Conn) Close() error {
	return c.conn.Close()
}

func (c *Conn) LocalAddr() net.Addr  { return c.conn.LocalAddr() }
func (c *Conn) RemoteAddr() net.Addr { return c.conn.RemoteAddr() }

func nonceBytes(v uint64) []byte {
	var n [12]byte
	binary.BigEndian.PutUint64(n[4:], v)
	return n[:]
}

func chpOverhead() int {
	// ChaCha20Poly1305 overhead is 16 bytes (tag)
	return 16
}

// SetDeadline sets read and write deadlines on the underlying connection.
func (c *Conn) SetDeadline(t time.Time) error { return c.conn.SetDeadline(t) }
func (c *Conn) SetReadDeadline(t time.Time) error { return c.conn.SetReadDeadline(t) }
func (c *Conn) SetWriteDeadline(t time.Time) error { return c.conn.SetWriteDeadline(t) }