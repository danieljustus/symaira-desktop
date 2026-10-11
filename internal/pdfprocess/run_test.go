package pdfprocess

import (
	"io"
	"strings"
	"testing"
)

func TestCaptureLimitCannotBeBypassedByCopy(t *testing.T) {
	overflow := make(chan struct{}, 1)
	c := &capture{overflow: overflow}
	// Strip WriterTo so io.Copy would select a promoted bytes.Buffer.ReadFrom.
	r := struct{ io.Reader }{strings.NewReader(strings.Repeat("x", CaptureLimit+1))}
	if n, err := io.Copy(c, r); err != nil || n != CaptureLimit+1 {
		t.Fatalf("drain: bytes=%d error=%v", n, err)
	}
	if c.buffer.Len() != CaptureLimit {
		t.Fatalf("capture retained %d bytes, limit %d", c.buffer.Len(), CaptureLimit)
	}
	select {
	case <-overflow:
	default:
		t.Fatal("overflow was not reported")
	}
}
