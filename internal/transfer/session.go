// Package transfer implements the application-level wire protocol used to
// stripe one logical data transfer across multiple independent QUIC
// connections (paths), and to reassemble + verify it on the receiving end.
package transfer

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"io"
)

// SessionID correlates multiple independent QUIC connections (one per path)
// into a single logical transfer.
type SessionID [16]byte

func NewSessionID() SessionID {
	var id SessionID
	if _, err := rand.Read(id[:]); err != nil {
		panic(err) // crypto/rand.Read only fails if the OS RNG is broken
	}
	return id
}

// ControlHeader is sent once, as the first message on path index 0, and
// describes the whole transfer.
type ControlHeader struct {
	SessionID SessionID
	TotalSize uint64
	ChunkSize uint32
	NumPaths  uint32
	Scheduler string
}

func (h ControlHeader) Encode(w io.Writer) error {
	if err := binary.Write(w, binary.BigEndian, h.SessionID); err != nil {
		return err
	}
	if err := binary.Write(w, binary.BigEndian, h.TotalSize); err != nil {
		return err
	}
	if err := binary.Write(w, binary.BigEndian, h.ChunkSize); err != nil {
		return err
	}
	if err := binary.Write(w, binary.BigEndian, h.NumPaths); err != nil {
		return err
	}
	if err := binary.Write(w, binary.BigEndian, uint16(len(h.Scheduler))); err != nil {
		return err
	}
	_, err := io.WriteString(w, h.Scheduler)
	return err
}

func ReadControlHeader(r io.Reader) (ControlHeader, error) {
	var h ControlHeader
	if err := binary.Read(r, binary.BigEndian, &h.SessionID); err != nil {
		return h, err
	}
	if err := binary.Read(r, binary.BigEndian, &h.TotalSize); err != nil {
		return h, err
	}
	if err := binary.Read(r, binary.BigEndian, &h.ChunkSize); err != nil {
		return h, err
	}
	if err := binary.Read(r, binary.BigEndian, &h.NumPaths); err != nil {
		return h, err
	}
	var schedLen uint16
	if err := binary.Read(r, binary.BigEndian, &schedLen); err != nil {
		return h, err
	}
	sched := make([]byte, schedLen)
	if _, err := io.ReadFull(r, sched); err != nil {
		return h, err
	}
	h.Scheduler = string(sched)
	return h, nil
}

// PathHello is sent once, as the first message on every path connection
// other than path index 0, so the server can correlate it to the session
// that the control header (on path 0) already described.
type PathHello struct {
	SessionID SessionID
	PathIndex uint32
}

func (p PathHello) Encode(w io.Writer) error {
	if err := binary.Write(w, binary.BigEndian, p.SessionID); err != nil {
		return err
	}
	return binary.Write(w, binary.BigEndian, p.PathIndex)
}

func ReadPathHello(r io.Reader) (PathHello, error) {
	var p PathHello
	if err := binary.Read(r, binary.BigEndian, &p.SessionID); err != nil {
		return p, err
	}
	if err := binary.Read(r, binary.BigEndian, &p.PathIndex); err != nil {
		return p, err
	}
	return p, nil
}

// pathIndexHeader is the one-byte discriminator every path stream starts
// with, right before the SessionID: 0 means "this is path 0, a
// ControlHeader follows", 1 means "a PathHello follows".
type streamKind uint8

const (
	streamKindControl streamKind = 0
	streamKindPath    streamKind = 1
)

func writeStreamKind(w io.Writer, k streamKind) error {
	_, err := w.Write([]byte{byte(k)})
	return err
}

func readStreamKind(r io.Reader) (streamKind, error) {
	var b [1]byte
	if _, err := io.ReadFull(r, b[:]); err != nil {
		return 0, err
	}
	if b[0] != byte(streamKindControl) && b[0] != byte(streamKindPath) {
		return 0, fmt.Errorf("transfer: invalid stream kind byte %d", b[0])
	}
	return streamKind(b[0]), nil
}

// WriteControlHeader writes the path-0 preamble (kind byte + header).
func WriteControlHeader(w io.Writer, h ControlHeader) error {
	if err := writeStreamKind(w, streamKindControl); err != nil {
		return err
	}
	return h.Encode(w)
}

// WritePathHello writes the non-zero-path preamble (kind byte + hello).
func WritePathHello(w io.Writer, p PathHello) error {
	if err := writeStreamKind(w, streamKindPath); err != nil {
		return err
	}
	return p.Encode(w)
}

// ReadPreamble reads whichever preamble is next on the stream and reports
// which one it was.
func ReadPreamble(r io.Reader) (ctrl *ControlHeader, hello *PathHello, err error) {
	kind, err := readStreamKind(r)
	if err != nil {
		return nil, nil, err
	}
	if kind == streamKindControl {
		h, err := ReadControlHeader(r)
		if err != nil {
			return nil, nil, err
		}
		return &h, nil, nil
	}
	p, err := ReadPathHello(r)
	if err != nil {
		return nil, nil, err
	}
	return nil, &p, nil
}
