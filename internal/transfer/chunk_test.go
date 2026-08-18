package transfer

import (
	"bytes"
	"testing"
)

func TestSplitAndReassemble(t *testing.T) {
	payload, hash, err := GenerateRandomPayload(100_000)
	if err != nil {
		t.Fatalf("GenerateRandomPayload: %v", err)
	}
	chunks := Split(payload, 4096)

	r := NewReassembler(uint64(len(payload)), 4096)
	for _, c := range chunks {
		r.Write(c)
	}
	if !r.VerifyHash(hash) {
		t.Fatal("reassembled payload hash mismatch")
	}
	if !bytes.Equal(r.Bytes(), payload) {
		t.Fatal("reassembled payload bytes mismatch")
	}
	if got := r.ReceivedBytes(); got != uint64(len(payload)) {
		t.Fatalf("ReceivedBytes = %d, want %d", got, len(payload))
	}
}

func TestReassemblerOutOfOrderAndDuplicate(t *testing.T) {
	payload, hash, err := GenerateRandomPayload(10_000)
	if err != nil {
		t.Fatalf("GenerateRandomPayload: %v", err)
	}
	chunks := Split(payload, 1000)

	r := NewReassembler(uint64(len(payload)), 1000)
	// reverse order, plus a duplicate of the first chunk written
	var complete bool
	for i := len(chunks) - 1; i >= 0; i-- {
		complete = r.Write(chunks[i])
	}
	complete = r.Write(chunks[len(chunks)-1]) || complete // duplicate write, must be idempotent
	if !complete {
		t.Fatal("reassembler never reported complete")
	}
	if !r.VerifyHash(hash) {
		t.Fatal("reassembled payload hash mismatch after out-of-order + duplicate writes")
	}
	if got := r.ReceivedBytes(); got != uint64(len(payload)) {
		t.Fatalf("ReceivedBytes = %d, want %d (duplicate must not double-count)", got, len(payload))
	}
}

func TestChunkWireRoundTrip(t *testing.T) {
	c := Chunk{Seq: 7, Offset: 12345, Data: []byte("hello world")}
	var buf bytes.Buffer
	if err := WriteChunk(&buf, c); err != nil {
		t.Fatalf("WriteChunk: %v", err)
	}
	got, err := ReadChunk(&buf)
	if err != nil {
		t.Fatalf("ReadChunk: %v", err)
	}
	if got.Seq != c.Seq || got.Offset != c.Offset || !bytes.Equal(got.Data, c.Data) {
		t.Fatalf("round-tripped chunk = %+v, want %+v", got, c)
	}
}
