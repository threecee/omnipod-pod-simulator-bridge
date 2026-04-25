// Package bridge implements the stdio I/O bridge between the
// CoreBluetooth-Mock-driven iOS test harness and the existing Pi simulator
// pod state machine. Replaces pkg/bluetooth/ which spoke BlueZ.
package bridge

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"sync"
)

type MsgType byte

// Central -> Pod (Swift -> Go): 0x00 .. 0x7F
const (
	MsgConnect     MsgType = 0x01
	MsgDisconnect  MsgType = 0x02
	MsgSubscribe   MsgType = 0x03
	MsgUnsubscribe MsgType = 0x04
	MsgWrite       MsgType = 0x05
	MsgReadRequest MsgType = 0x06
)

// Pod -> Central (Go -> Swift): 0x80 .. 0xEF
const (
	MsgConnectAck    MsgType = 0x81
	MsgDisconnectAck MsgType = 0x82
	MsgNotify        MsgType = 0x83
	MsgReadResponse  MsgType = 0x84
	MsgError         MsgType = 0x85
)

// Debug-only: 0xF0 ..
const (
	MsgDebugGetLastCommand MsgType = 0xF0
	MsgDebugLastCommand    MsgType = 0xF1
)

const MaxPayloadSize = 1 << 20 // 1 MiB safety cap

func WriteFrame(w io.Writer, t MsgType, payload []byte) error {
	if len(payload) > MaxPayloadSize {
		return fmt.Errorf("payload too large: %d > %d", len(payload), MaxPayloadSize)
	}
	header := [5]byte{byte(t)}
	binary.BigEndian.PutUint32(header[1:], uint32(len(payload)))
	if _, err := w.Write(header[:]); err != nil {
		return err
	}
	if len(payload) > 0 {
		if _, err := w.Write(payload); err != nil {
			return err
		}
	}
	return nil
}

// FrameWriter is a frame-atomic, goroutine-safe wrapper around an io.Writer.
//
// The dispatch loop and the BridgeBle drain goroutines all emit frames to
// stdout concurrently. WriteFrame's two-call pattern (header then payload)
// is NOT atomic against a bare mutex on the underlying io.Writer — a writer
// could grab the mutex *between* another writer's header and payload, which
// would desync the stream on the central side.
//
// FrameWriter.WriteFrame builds the full frame in a buffer first, then
// performs ONE Write under the mutex, guaranteeing each frame appears as a
// contiguous run of bytes on the wire. All concurrent producers MUST go
// through *FrameWriter — no caller should write to the underlying io.Writer
// directly.
type FrameWriter struct {
	mu sync.Mutex
	w  io.Writer
}

// NewFrameWriter wraps w. Concurrent callers of WriteFrame are safe; the
// underlying w is touched only under the FrameWriter's mutex.
func NewFrameWriter(w io.Writer) *FrameWriter {
	return &FrameWriter{w: w}
}

// WriteFrame builds the [type:1][length:4 BE][payload] frame in a buffer
// and writes it atomically to the underlying io.Writer.
func (fw *FrameWriter) WriteFrame(t MsgType, payload []byte) error {
	if len(payload) > MaxPayloadSize {
		return fmt.Errorf("payload too large: %d > %d", len(payload), MaxPayloadSize)
	}
	var buf bytes.Buffer
	buf.Grow(5 + len(payload))
	if err := WriteFrame(&buf, t, payload); err != nil {
		return err
	}
	fw.mu.Lock()
	defer fw.mu.Unlock()
	_, err := fw.w.Write(buf.Bytes())
	return err
}

func ReadFrame(r io.Reader) (MsgType, []byte, error) {
	var header [5]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		if errors.Is(err, io.EOF) {
			return 0, nil, io.EOF
		}
		return 0, nil, fmt.Errorf("read header: %w", err)
	}
	t := MsgType(header[0])
	length := binary.BigEndian.Uint32(header[1:])
	if length > MaxPayloadSize {
		return 0, nil, fmt.Errorf("payload length %d exceeds max %d", length, MaxPayloadSize)
	}
	if length == 0 {
		return t, nil, nil
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(r, payload); err != nil {
		return 0, nil, fmt.Errorf("read payload: %w", err)
	}
	return t, payload, nil
}
