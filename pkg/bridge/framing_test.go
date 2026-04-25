package bridge

import (
	"bytes"
	"testing"
)

func TestFramingRoundTrip_emptyPayload(t *testing.T) {
	var buf bytes.Buffer
	err := WriteFrame(&buf, MsgConnectAck, nil)
	if err != nil {
		t.Fatalf("WriteFrame: %v", err)
	}
	msgType, payload, err := ReadFrame(&buf)
	if err != nil {
		t.Fatalf("ReadFrame: %v", err)
	}
	if msgType != MsgConnectAck {
		t.Errorf("type: got 0x%02x, want 0x%02x", msgType, MsgConnectAck)
	}
	if len(payload) != 0 {
		t.Errorf("payload: got %d bytes, want 0", len(payload))
	}
}

func TestFramingRoundTrip_withPayload(t *testing.T) {
	var buf bytes.Buffer
	want := []byte{0x01, 0x02, 0x03, 0x04, 0x05}
	err := WriteFrame(&buf, MsgWrite, want)
	if err != nil {
		t.Fatalf("WriteFrame: %v", err)
	}
	msgType, got, err := ReadFrame(&buf)
	if err != nil {
		t.Fatalf("ReadFrame: %v", err)
	}
	if msgType != MsgWrite {
		t.Errorf("type: got 0x%02x, want 0x%02x", msgType, MsgWrite)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("payload: got %v, want %v", got, want)
	}
}

func TestFraming_truncatedHeader(t *testing.T) {
	buf := bytes.NewReader([]byte{0x05, 0x00, 0x00})
	_, _, err := ReadFrame(buf)
	if err == nil {
		t.Fatal("expected error on truncated header, got nil")
	}
}

func TestFraming_truncatedPayload(t *testing.T) {
	buf := bytes.NewReader([]byte{0x05, 0x00, 0x00, 0x00, 0x64, 0xAA, 0xBB, 0xCC, 0xDD, 0xEE})
	_, _, err := ReadFrame(buf)
	if err == nil {
		t.Fatal("expected error on truncated payload, got nil")
	}
}

func TestFraming_zeroLengthPayload(t *testing.T) {
	buf := bytes.NewReader([]byte{0x82, 0x00, 0x00, 0x00, 0x00})
	msgType, payload, err := ReadFrame(buf)
	if err != nil {
		t.Fatalf("ReadFrame: %v", err)
	}
	if msgType != MsgDisconnectAck {
		t.Errorf("type: got 0x%02x, want 0x%02x", msgType, MsgDisconnectAck)
	}
	if len(payload) != 0 {
		t.Errorf("payload: got %d bytes, want 0", len(payload))
	}
}
