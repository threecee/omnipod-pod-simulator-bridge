// Cross-platform type definitions for the bluetooth package. The full
// production *Ble implementation lives in bluetooth.go, which is gated to
// Linux (it depends on paypal/gatt + BlueZ). These types are needed by
// pkg/pod's BleInterface and by the stdio bridge alternative implementation,
// so they must compile on macOS too.

package bluetooth

import "encoding/hex"

type Packet []byte

var (
	CmdRTS     = Packet([]byte{0})
	CmdCTS     = Packet([]byte{1})
	CmdNACK    = Packet([]byte{2, 0})
	CmdAbort   = Packet([]byte{3})
	CmdSuccess = Packet([]byte{4})
	CmdFail    = Packet([]byte{5})
)

func (p Packet) String() string {
	return hex.EncodeToString(p)
}
