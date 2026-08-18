package transfer

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"io"
	"sync"
)

// GenerateRandomPayload creates size bytes of random data.
func GenerateRandomPayload(size uint64) ([]byte, error) {
	data := make([]byte, size)
	if _, err := rand.Read(data); err != nil {
		return nil, fmt.Errorf("transfer: generating random payload: %w", err)
	}
	return data, nil
}

// Chunk is one piece of the payload, addressed by its byte offset so the
// receiver can place it correctly regardless of arrival order across paths.
// Checksum is a CRC32 of Data, verified independently per chunk -- there is
// no whole-transfer hash, so chunks don't need a known total size or a
// uniform size, and verification never has to wait for a "last" chunk.
type Chunk struct {
	Seq      uint64
	Offset   uint64
	Checksum uint32
	Data     []byte
}

// VerifyChecksum reports whether Data still matches Checksum.
func (c Chunk) VerifyChecksum() bool {
	return crc32.ChecksumIEEE(c.Data) == c.Checksum
}

// Split slices data into chunkSize-sized chunks (the last one may be
// smaller).
func Split(data []byte, chunkSize uint32) []Chunk {
	if chunkSize == 0 {
		chunkSize = uint32(len(data))
	}
	var chunks []Chunk
	var seq uint64
	for offset := 0; offset < len(data); offset += int(chunkSize) {
		end := offset + int(chunkSize)
		if end > len(data) {
			end = len(data)
		}
		chunks = append(chunks, Chunk{Seq: seq, Offset: uint64(offset), Data: data[offset:end]})
		seq++
	}
	return chunks
}

// WriteChunk writes one length-prefixed chunk frame to a path stream. The
// checksum is computed here from c.Data, so callers never set it themselves.
func WriteChunk(w io.Writer, c Chunk) error {
	if err := binary.Write(w, binary.BigEndian, c.Seq); err != nil {
		return err
	}
	if err := binary.Write(w, binary.BigEndian, c.Offset); err != nil {
		return err
	}
	if err := binary.Write(w, binary.BigEndian, uint32(len(c.Data))); err != nil {
		return err
	}
	if err := binary.Write(w, binary.BigEndian, crc32.ChecksumIEEE(c.Data)); err != nil {
		return err
	}
	_, err := w.Write(c.Data)
	return err
}

// ReadChunk reads one length-prefixed chunk frame from a path stream. It
// does not verify the checksum -- call Chunk.VerifyChecksum, since a
// mismatch is a data-integrity event for the caller to count, not a framing
// error that should abort reading the rest of the stream.
func ReadChunk(r io.Reader) (Chunk, error) {
	var c Chunk
	if err := binary.Read(r, binary.BigEndian, &c.Seq); err != nil {
		return c, err
	}
	if err := binary.Read(r, binary.BigEndian, &c.Offset); err != nil {
		return c, err
	}
	var n uint32
	if err := binary.Read(r, binary.BigEndian, &n); err != nil {
		return c, err
	}
	if err := binary.Read(r, binary.BigEndian, &c.Checksum); err != nil {
		return c, err
	}
	c.Data = make([]byte, n)
	if _, err := io.ReadFull(r, c.Data); err != nil {
		return c, err
	}
	return c, nil
}

// Reassembler collects chunks (possibly arriving out of order, possibly
// duplicated by a redundant scheduler) into one buffer.
//
// ponytail: whole payload is buffered in memory; for transfer sizes beyond
// available RAM, switch the backing buffer to a temp file opened with
// os.CreateTemp and write chunks via WriteAt instead.
type Reassembler struct {
	mu            sync.Mutex
	buf           []byte
	received      []bool // by chunk seq
	chunkSize     uint32
	remaining     int
	receivedBytes uint64
}

func NewReassembler(totalSize uint64, chunkSize uint32) *Reassembler {
	if chunkSize == 0 {
		chunkSize = uint32(totalSize)
	}
	numChunks := int((totalSize + uint64(chunkSize) - 1) / uint64(chunkSize))
	return &Reassembler{
		buf:       make([]byte, totalSize),
		received:  make([]bool, numChunks),
		chunkSize: chunkSize,
		remaining: numChunks,
	}
}

// Write places a chunk's data at its offset. It's safe to call concurrently
// from multiple path goroutines and idempotent for duplicate chunks (from a
// redundant scheduler). Returns true once every chunk has been received at
// least once.
func (r *Reassembler) Write(c Chunk) (complete bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	seq := int(c.Offset / uint64(r.chunkSize))
	if seq < 0 || seq >= len(r.received) {
		return r.remaining == 0
	}
	if !r.received[seq] {
		copy(r.buf[c.Offset:], c.Data)
		r.received[seq] = true
		r.remaining--
		r.receivedBytes += uint64(len(c.Data))
	}
	return r.remaining == 0
}

// ReceivedBytes returns the number of unique payload bytes written so far
// (duplicates from a redundant scheduler are not double-counted).
func (r *Reassembler) ReceivedBytes() uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.receivedBytes
}

// Bytes returns the reassembled payload. Only meaningful once Write has
// reported complete.
func (r *Reassembler) Bytes() []byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.buf
}

// Complete reports whether every chunk has been received at least once.
func (r *Reassembler) Complete() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.remaining == 0
}
