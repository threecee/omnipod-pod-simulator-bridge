package bridge

import (
	"github.com/avereha/pod/pkg/bluetooth"
)

// Pod GATT characteristic UUIDs (matching pkg/bluetooth/bluetooth.go).
//
// CMD characteristic carries control packets (RTS/CTS/SUCCESS/FAIL/etc.),
// DATA characteristic carries fragmented message bytes. The Swift CBM mock
// peripheral on the central side speaks the same UUIDs.

// 1a7e-2441-e3ed-4464-8b7e-751e03d0dc5f
var CmdCharUUID = [16]byte{
	0x1a, 0x7e, 0x24, 0x41, 0xe3, 0xed, 0x44, 0x64,
	0x8b, 0x7e, 0x75, 0x1e, 0x03, 0xd0, 0xdc, 0x5f,
}

// 1a7e-2442-e3ed-4464-8b7e-751e03d0dc5f
var DataCharUUID = [16]byte{
	0x1a, 0x7e, 0x24, 0x42, 0xe3, 0xed, 0x44, 0x64,
	0x8b, 0x7e, 0x75, 0x1e, 0x03, 0xd0, 0xdc, 0x5f,
}

// BridgeBle is a *bluetooth.BleCore-backed implementation of pod.BleInterface
// that gets its incoming GATT packets from the stdio bridge and emits
// outgoing GATT packets back through the bridge.
//
// It embeds *bluetooth.BleCore so pod.BleInterface (implemented by BleCore)
// is satisfied directly. The bridge's OnWrite handler calls PushCmd/PushData
// based on charUUID; a goroutine drains the outgoing channels and emits
// NOTIFY frames.
type BridgeBle struct {
	*bluetooth.BleCore
}

func NewBridgeBle() *BridgeBle {
	return &BridgeBle{BleCore: bluetooth.NewBleCore()}
}

// RouteIncoming dispatches a raw GATT write packet into the appropriate
// inbound channel based on the characteristic UUID. Returns false if the
// UUID is unrecognized.
func (b *BridgeBle) RouteIncoming(charUUID [16]byte, data []byte) bool {
	switch charUUID {
	case CmdCharUUID:
		b.PushCmd(bluetooth.Packet(data))
		return true
	case DataCharUUID:
		b.PushData(bluetooth.Packet(data))
		return true
	default:
		return false
	}
}

// ShutdownConnection stops BleCore's message loop so the pod state
// machine's restart path in CommandLoop (after a 1-minute idle
// ReadMessageWithTimeout) can call StartMessageLoop again without
// tripping core.go's "Messaging loop is already running" log.Fatalf.
//
// We do NOT actually tear down the bridge transport here — the Swift
// side controls connect/disconnect via CONNECT/DISCONNECT frames. We
// just stop the message loop; the next StartAcceptingCommands call
// will spin a fresh one.
//
// (Code review C2: previously a no-op, which caused log.Fatalf on the
// subsequent StartMessageLoop after any 60-second idle period in iOS
// test harnesses.)
func (b *BridgeBle) ShutdownConnection() {
	b.StopMessageLoop()
}

// RefreshAdvertisingWithSpecifiedId is a no-op for the bridge — advertising
// is handled at the CBM layer in Swift.
func (b *BridgeBle) RefreshAdvertisingWithSpecifiedId(id []byte) error {
	return nil
}
