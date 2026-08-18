package transfer

import (
	"bytes"
	"testing"
)

func TestControlHeaderWireRoundTrip(t *testing.T) {
	h := ControlHeader{
		SessionID: NewSessionID(),
		TotalSize: 123456,
		ChunkSize: 4096,
		NumPaths:  2,
		Scheduler: "roundrobin",
	}
	var buf bytes.Buffer
	if err := WriteControlHeader(&buf, h); err != nil {
		t.Fatalf("WriteControlHeader: %v", err)
	}
	ctrl, hello, err := ReadPreamble(&buf)
	if err != nil {
		t.Fatalf("ReadPreamble: %v", err)
	}
	if hello != nil {
		t.Fatal("expected a ControlHeader, got a PathHello")
	}
	if *ctrl != h {
		t.Fatalf("round-tripped header = %+v, want %+v", *ctrl, h)
	}
}

func TestPathHelloWireRoundTrip(t *testing.T) {
	p := PathHello{SessionID: NewSessionID(), PathIndex: 3}
	var buf bytes.Buffer
	if err := WritePathHello(&buf, p); err != nil {
		t.Fatalf("WritePathHello: %v", err)
	}
	ctrl, hello, err := ReadPreamble(&buf)
	if err != nil {
		t.Fatalf("ReadPreamble: %v", err)
	}
	if ctrl != nil {
		t.Fatal("expected a PathHello, got a ControlHeader")
	}
	if *hello != p {
		t.Fatalf("round-tripped hello = %+v, want %+v", *hello, p)
	}
}
