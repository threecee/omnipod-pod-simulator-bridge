// Package bridge implements the stdio I/O bridge between the
// CoreBluetooth-Mock-driven iOS test harness and the existing Pi simulator
// pod state machine. Replaces pkg/bluetooth/ which spoke BlueZ.
package bridge

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
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
