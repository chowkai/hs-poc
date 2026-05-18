package noise

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"log"

	"golang.org/x/net/http2/hpack"
)

type H2Frame struct {
	Length   uint32
	Type     byte
	Flags    byte
	StreamID uint32
	Payload  []byte
}

func ReadH2Frame(r io.Reader) (*H2Frame, error) {
	var hdr [9]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return nil, err
	}
	f := &H2Frame{
		Length:   uint32(hdr[0])<<16 | uint32(hdr[1])<<8 | uint32(hdr[2]),
		Type:     hdr[3],
		Flags:    hdr[4],
		StreamID: binary.BigEndian.Uint32(hdr[5:9]) & 0x7FFFFFFF,
	}
	f.Payload = make([]byte, f.Length)
	if f.Length > 0 {
		if _, err := io.ReadFull(r, f.Payload); err != nil {
			return nil, err
		}
	}
	log.Printf("[H2 READ] type=%d flags=%d stream=%d len=%d", f.Type, f.Flags, f.StreamID, f.Length)
	return f, nil
}

// readH2FrameCtx calls ReadH2Frame but returns ctx.Err() if ctx is done first.
func readH2FrameCtx(ctx context.Context, r io.Reader) (*H2Frame, error) {
	type res struct {
		f   *H2Frame
		err error
	}
	ch := make(chan res, 1)
	go func() {
		f, err := ReadH2Frame(r)
		ch <- res{f, err}
	}()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case r := <-ch:
		return r.f, r.err
	}
}

func WriteH2Frame(w io.Writer, length uint32, ftype byte, flags byte, streamID uint32, payload []byte) error {
	log.Printf("[H2 WRITE] type=%d flags=%d stream=%d len=%d", ftype, flags, streamID, length)
	var hdr [9]byte
	hdr[0] = byte(length >> 16)
	hdr[1] = byte(length >> 8)
	hdr[2] = byte(length)
	hdr[3] = ftype
	hdr[4] = flags
	binary.BigEndian.PutUint32(hdr[5:9], streamID)
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	if len(payload) > 0 {
		if _, err := w.Write(payload); err != nil {
			return err
		}
	}
	return nil
}

const (
	FrameData         = 0x0
	FrameHeaders      = 0x1
	FrameSettings     = 0x4
	FrameWindowUpdate = 0x8

	FlagEndStream  = 0x1
	FlagEndHeaders = 0x4
	FlagAck        = 0x1
)

type H2Conn struct {
	rwc        io.ReadWriteCloser
	enc        *hpack.Encoder
	encBuf     *hpackEncoderBuf
	dec        *hpack.Decoder
	nextStream uint32
}

func NewH2Conn(rwc io.ReadWriteCloser) (*H2Conn, error) {
	var peek [9]byte
	if _, err := io.ReadFull(rwc, peek[:]); err != nil {
		return nil, fmt.Errorf("read preamble: %w", err)
	}

	const earlyPayloadMagic = "\xff\xff\xffTS"
	if string(peek[:5]) == earlyPayloadMagic {
		payLen := int(peek[5])<<24 | int(peek[6])<<16 | int(peek[7])<<8 | int(peek[8])
		if payLen > 65536 {
			return nil, fmt.Errorf("early payload too large: %d", payLen)
		}
		if payLen > 0 {
			if _, err := io.CopyN(io.Discard, rwc, int64(payLen)); err != nil {
				return nil, fmt.Errorf("read early payload: %w", err)
			}
		}
		return initH2Conn(rwc)
	}
	return initH2ConnWithHeader(rwc, peek[:])
}

const h2Preface = "PRI * HTTP/2.0\r\n\r\nSM\r\n\r\n"

func initH2Conn(rwc io.ReadWriteCloser) (*H2Conn, error) {
	// Send HTTP/2 connection preface (required after noise upgrade)
	if _, err := io.WriteString(rwc, h2Preface); err != nil {
		return nil, fmt.Errorf("write h2 preface: %w", err)
	}
	f, err := ReadH2Frame(rwc)
	if err != nil {
		return nil, fmt.Errorf("read server settings: %w", err)
	}
	return finishH2Init(rwc, f)
}

func initH2ConnWithHeader(rwc io.ReadWriteCloser, hdr []byte) (*H2Conn, error) {
	// Send HTTP/2 connection preface first
	if _, err := io.WriteString(rwc, h2Preface); err != nil {
		return nil, fmt.Errorf("write h2 preface: %w", err)
	}
	f := &H2Frame{
		Length:   uint32(hdr[0])<<16 | uint32(hdr[1])<<8 | uint32(hdr[2]),
		Type:     hdr[3],
		Flags:    hdr[4],
		StreamID: binary.BigEndian.Uint32(hdr[5:9]) & 0x7FFFFFFF,
	}
	f.Payload = make([]byte, f.Length)
	if f.Length > 0 {
		if _, err := io.ReadFull(rwc, f.Payload); err != nil {
			return nil, fmt.Errorf("read settings payload: %w", err)
		}
	}
	return finishH2Init(rwc, f)
}

func finishH2Init(rwc io.ReadWriteCloser, f *H2Frame) (*H2Conn, error) {
	if f.Type == FrameSettings {
		if err := WriteH2Frame(rwc, 0, FrameSettings, FlagAck, 0, nil); err != nil {
			return nil, fmt.Errorf("write settings ack: %w", err)
		}
	}
	if err := WriteH2Frame(rwc, 0, FrameSettings, 0, 0, nil); err != nil {
		return nil, fmt.Errorf("write settings: %w", err)
	}

	dec := hpack.NewDecoder(4096, nil)
	encBuf := &hpackEncoderBuf{}
	enc := hpack.NewEncoder(encBuf)

	return &H2Conn{
		rwc:        rwc,
		enc:        enc,
		encBuf:     encBuf,
		dec:        dec,
		nextStream: 1,
	}, nil
}

func (h *H2Conn) DoRequest(method, path, host string, reqBody []byte) (int, []byte, error) {
	return h.DoRequestContext(context.Background(), method, path, host, reqBody)
}

// DoRequestContext is like DoRequest but cancels when ctx is done, returning any data received so far.
func (h *H2Conn) DoRequestContext(ctx context.Context, method, path, host string, reqBody []byte) (int, []byte, error) {
	streamID := h.nextStream
	h.nextStream += 2

	hdrs := []hpack.HeaderField{
		{Name: ":method", Value: method},
		{Name: ":scheme", Value: "https"},
		{Name: ":authority", Value: host},
		{Name: ":path", Value: path},
		{Name: "content-type", Value: "application/json"},
	}

	h.encBuf.Reset()
	for _, hf := range hdrs {
		h.enc.WriteField(hf)
	}
	hdrBlock := h.encBuf.Bytes()

	flags := byte(FlagEndHeaders | FlagEndStream)
	if len(reqBody) > 0 {
		flags = FlagEndHeaders
	}

	if err := WriteH2Frame(h.rwc, uint32(len(hdrBlock)), FrameHeaders, flags, streamID, hdrBlock); err != nil {
		return 0, nil, fmt.Errorf("write request headers: %w", err)
	}

	if len(reqBody) > 0 {
		if err := WriteH2Frame(h.rwc, uint32(len(reqBody)), FrameData, FlagEndStream, streamID, reqBody); err != nil {
			return 0, nil, fmt.Errorf("write request data: %w", err)
		}
	}

	h.dec.SetMaxStringLength(1 << 20)
	var statusCode int
	var body []byte

	for {
		f, err := readH2FrameCtx(ctx, h.rwc)
		if err != nil {
			if ctx.Err() != nil && statusCode != 0 && len(body) > 0 {
				return statusCode, body, nil
			}
			return 0, nil, fmt.Errorf("read response: %w", err)
		}

		if f.StreamID != streamID {
			if f.StreamID == 0 {
				switch f.Type {
				case FrameSettings:
					if f.Flags&FlagAck == 0 {
						WriteH2Frame(h.rwc, 0, FrameSettings, FlagAck, 0, nil)
					}
				}
			}
			continue
		}

		switch f.Type {
		case FrameHeaders:
			hfs, err := h.dec.DecodeFull(f.Payload)
			if err != nil {
				return 0, nil, fmt.Errorf("decode response headers: %w", err)
			}
			for _, hf := range hfs {
				if hf.Name == ":status" {
					fmt.Sscanf(hf.Value, "%d", &statusCode)
				}
			}
			if f.Flags&FlagEndStream != 0 {
				return statusCode, body, nil
			}

		case FrameData:
			body = append(body, f.Payload...)
			if f.Flags&FlagEndStream != 0 {
				return statusCode, body, nil
			}
		}
	}
}

func (h *H2Conn) Close() error {
	var payload [8]byte
	WriteH2Frame(h.rwc, 8, 0x7, 0, 0, payload[:])
	return h.rwc.Close()
}

type hpackEncoderBuf struct {
	buf []byte
}

func (b *hpackEncoderBuf) Write(p []byte) (int, error) {
	b.buf = append(b.buf, p...)
	return len(p), nil
}

func (b *hpackEncoderBuf) Bytes() []byte { return b.buf }
func (b *hpackEncoderBuf) Reset()        { b.buf = nil }