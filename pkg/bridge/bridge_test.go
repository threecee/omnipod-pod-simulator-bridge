package bridge

import (
	"bytes"
	"io"
	"sync"
	"testing"
)

type fakePod struct {
	connected     bool
	disconnected  bool
	writeCalls    [][]byte
	notifyOnWrite []byte
}

func (p *fakePod) OnConnect() error    { p.connected = true; return nil }
func (p *fakePod) OnDisconnect() error { p.disconnected = true; return nil }
func (p *fakePod) OnWrite(charUUID [16]byte, data []byte) ([]byte, error) {
	p.writeCalls = append(p.writeCalls, data)
	return p.notifyOnWrite, nil
}

func TestBridge_connectFlow(t *testing.T) {
	pod := &fakePod{}
	in := &bytes.Buffer{}
	out := &bytes.Buffer{}
	WriteFrame(in, MsgConnect, nil)
	br := New(pod)
	br.Run(in, NewFrameWriter(out), io.Discard)
	if !pod.connected {
		t.Error("pod.OnConnect() never called")
	}
	msgType, _, err := ReadFrame(out)
	if err != nil {
		t.Fatalf("ReadFrame from out: %v", err)
	}
	if msgType != MsgConnectAck {
		t.Errorf("type: got 0x%02x, want 0x%02x", msgType, MsgConnectAck)
	}
}

func TestBridge_writeForwardsToPod(t *testing.T) {
	pod := &fakePod{notifyOnWrite: []byte{0xAA, 0xBB}}
	in := &bytes.Buffer{}
	out := &bytes.Buffer{}
	WriteFrame(in, MsgConnect, nil)
	writePayload := make([]byte, 16+3)
	writePayload[16] = 0xDE
	writePayload[17] = 0xAD
	writePayload[18] = 0xBE
	WriteFrame(in, MsgWrite, writePayload)
	br := New(pod)
	br.Run(in, NewFrameWriter(out), io.Discard)
	if len(pod.writeCalls) != 1 {
		t.Fatalf("expected 1 write call, got %d", len(pod.writeCalls))
	}
	if !bytes.Equal(pod.writeCalls[0], []byte{0xDE, 0xAD, 0xBE}) {
		t.Errorf("write data: got %x, want DEADBE", pod.writeCalls[0])
	}
	ReadFrame(out)
	msgType, payload, err := ReadFrame(out)
	if err != nil {
		t.Fatalf("ReadFrame for NOTIFY: %v", err)
	}
	if msgType != MsgNotify {
		t.Errorf("type: got 0x%02x, want 0x%02x (NOTIFY)", msgType, MsgNotify)
	}
	if len(payload) != 16+2 {
		t.Errorf("notify payload length: got %d, want 18", len(payload))
	}
	if !bytes.Equal(payload[16:], []byte{0xAA, 0xBB}) {
		t.Errorf("notify data: got %x, want AABB", payload[16:])
	}
}

func TestBridge_disconnectExitsCleanly(t *testing.T) {
	pod := &fakePod{}
	in := &bytes.Buffer{}
	out := &bytes.Buffer{}
	WriteFrame(in, MsgConnect, nil)
	WriteFrame(in, MsgDisconnect, nil)
	br := New(pod)
	br.Run(in, NewFrameWriter(out), io.Discard)
	if !pod.disconnected {
		t.Error("pod.OnDisconnect() never called")
	}
}

func TestBridge_eofExitsCleanly(t *testing.T) {
	pod := &fakePod{}
	in := &bytes.Buffer{}
	out := &bytes.Buffer{}
	br := New(pod)
	err := br.Run(in, NewFrameWriter(out), io.Discard)
	if err != nil {
		t.Errorf("Run on empty input: got %v, want nil", err)
	}
}

// TestBridge_concurrentWritesDoNotInterleave is the regression test for C1
// (frame interleaving). With the old plain-mutex safeWriter pattern, three
// concurrent producers calling the package-level WriteFrame (which does two
// underlying Writes for header + payload) could grab the mutex *between*
// another producer's header and payload, desyncing the stream.
//
// FrameWriter.WriteFrame builds the full frame in a buffer and emits it in
// one Write under the mutex. This test hammers FrameWriter from multiple
// goroutines and asserts (a) every frame is well-formed, (b) the total
// count is exact, and (c) the stream parses cleanly back into N frames.
//
// Run with `go test -race ./pkg/bridge/...` to also catch any future
// regressions in the mutex protocol.
func TestBridge_concurrentWritesDoNotInterleave(t *testing.T) {
	const writers = 3
	const framesPerWriter = 1000

	var out bytes.Buffer
	fw := NewFrameWriter(&out)

	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			payload := make([]byte, 16)
			payload[0] = byte(id)
			for j := 0; j < framesPerWriter; j++ {
				if err := fw.WriteFrame(MsgNotify, payload); err != nil {
					t.Errorf("writer %d frame %d: %v", id, j, err)
					return
				}
			}
		}(i)
	}
	wg.Wait()

	r := bytes.NewReader(out.Bytes())
	count := 0
	for {
		msgType, payload, err := ReadFrame(r)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("parse frame %d: %v", count, err)
		}
		if msgType != MsgNotify {
			t.Errorf("frame %d type: got 0x%02x, want 0x%02x", count, msgType, MsgNotify)
		}
		if len(payload) != 16 {
			t.Errorf("frame %d payload length: got %d, want 16", count, len(payload))
		}
		count++
	}
	if count != writers*framesPerWriter {
		t.Errorf("frame count: got %d, want %d", count, writers*framesPerWriter)
	}
}
