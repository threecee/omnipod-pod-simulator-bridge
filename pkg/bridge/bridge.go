package bridge

import (
	"errors"
	"fmt"
	"io"
)

type Pod interface {
	OnConnect() error
	OnDisconnect() error
	OnWrite(charUUID [16]byte, data []byte) ([]byte, error)
}

type Bridge struct {
	pod Pod
}

func New(p Pod) *Bridge {
	return &Bridge{pod: p}
}

func (b *Bridge) Run(in io.Reader, out io.Writer, logSink io.Writer) error {
	for {
		msgType, payload, err := ReadFrame(in)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("ReadFrame: %w", err)
		}
		if err := b.dispatch(msgType, payload, out, logSink); err != nil {
			errPayload := []byte(err.Error())
			_ = WriteFrame(out, MsgError, errPayload)
		}
	}
}

func (b *Bridge) dispatch(t MsgType, payload []byte, out io.Writer, logSink io.Writer) error {
	switch t {
	case MsgConnect:
		if err := b.pod.OnConnect(); err != nil {
			return fmt.Errorf("OnConnect: %w", err)
		}
		return WriteFrame(out, MsgConnectAck, nil)
	case MsgDisconnect:
		if err := b.pod.OnDisconnect(); err != nil {
			return fmt.Errorf("OnDisconnect: %w", err)
		}
		return WriteFrame(out, MsgDisconnectAck, nil)
	case MsgSubscribe, MsgUnsubscribe:
		return nil
	case MsgWrite:
		if len(payload) < 16 {
			return fmt.Errorf("WRITE payload too short: %d < 16 (UUID)", len(payload))
		}
		var charUUID [16]byte
		copy(charUUID[:], payload[:16])
		data := payload[16:]
		notify, err := b.pod.OnWrite(charUUID, data)
		if err != nil {
			return fmt.Errorf("OnWrite: %w", err)
		}
		if notify != nil {
			notifyPayload := make([]byte, 16+len(notify))
			copy(notifyPayload[:16], charUUID[:])
			copy(notifyPayload[16:], notify)
			return WriteFrame(out, MsgNotify, notifyPayload)
		}
		return nil
	case MsgReadRequest:
		if len(payload) < 16+4 {
			return fmt.Errorf("READ_REQUEST payload too short: %d < 20", len(payload))
		}
		reqID := payload[16:20]
		respPayload := append([]byte{}, reqID...)
		return WriteFrame(out, MsgReadResponse, respPayload)
	case MsgDebugGetLastCommand:
		return WriteFrame(out, MsgDebugLastCommand, nil)
	default:
		return fmt.Errorf("unknown message type: 0x%02x", byte(t))
	}
}
