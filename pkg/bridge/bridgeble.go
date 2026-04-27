package bridge

import (
	"sync"

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
//
// # Backpressure
//
// The drain goroutines in main.go (one for CMD, one for DATA) call PullCmd /
// PullData in a tight loop, writing each outgoing packet as a NOTIFY frame to
// stdout via FrameWriter. If stdout backs up (e.g. the Swift side is not
// reading), FrameWriter.WriteFrame blocks while holding no lock that would
// affect incoming traffic — but once the goroutine blocks on Write, it cannot
// call PullCmd/PullData, which means BleCore's internal cmdOutput/dataOutput
// channels fill up (capacity 5 each). Once those are full, BleCore's loop
// blocks on WriteCmd/WriteData, which in turn blocks the protocol loop
// (which runs in the same goroutine). Eventually the bridge dispatch loop
// blocks trying to push an incoming GATT packet. The entire pipeline stalls
// proportionally to how far stdout is backed up.
//
// If short write bursts need to be absorbed without stalling, wrapping stdout
// in a bufio.Writer before passing it to NewFrameWriter would add an
// application-level buffer. This is not done today because test throughput
// is low and the Swift side always reads promptly, but it is the correct
// lever to reach for if latency spikes appear under load.
//
// # Clean shutdown
//
// When Bridge.Run returns (stdin EOF), call Stop() so the drain goroutines
// unblock and return cleanly instead of leaking until process exit. The
// done channel returned by Done() is closed by Stop(); drain goroutines
// should select on it in their pull loops (see main.go's podAdapter.drain).
type BridgeBle struct {
	*bluetooth.BleCore

	stopOnce sync.Once
	done     chan struct{}
}

func NewBridgeBle() *BridgeBle {
	return &BridgeBle{
		BleCore: bluetooth.NewBleCore(),
		done:    make(chan struct{}),
	}
}

// Done returns a channel that is closed when Stop() is called. Drain
// goroutines should select on this channel alongside their pull calls so
// they exit cleanly when Bridge.Run returns on stdin EOF.
func (b *BridgeBle) Done() <-chan struct{} {
	return b.done
}

// Stop signals the drain goroutines to exit. It is idempotent; calling it
// more than once is safe. main.go calls this after Bridge.Run returns.
func (b *BridgeBle) Stop() {
	b.stopOnce.Do(func() { close(b.done) })
}

// PullCmdStopped returns the next outgoing CMD packet, or (nil, false) if
// Stop() has been called. Drain goroutines use this instead of PullCmd so
// they unblock and return cleanly on shutdown rather than blocking forever.
func (b *BridgeBle) PullCmdStopped() (bluetooth.Packet, bool) {
	select {
	case pkt := <-b.BleCore.CmdOutputCh():
		return pkt, true
	case <-b.done:
		return nil, false
	}
}

// PullDataStopped returns the next outgoing DATA packet, or (nil, false) if
// Stop() has been called. Drain goroutines use this for the same reason.
func (b *BridgeBle) PullDataStopped() (bluetooth.Packet, bool) {
	select {
	case pkt := <-b.BleCore.DataOutputCh():
		return pkt, true
	case <-b.done:
		return nil, false
	}
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
