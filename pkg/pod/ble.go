package pod

import (
	"time"

	"github.com/avereha/pod/pkg/bluetooth"
	"github.com/avereha/pod/pkg/message"
)

// BleInterface is the BLE I/O surface that pod.Pod requires. The production
// implementation is *bluetooth.Ble (Linux-only); the stdio bridge provides
// an alternative implementation backed by channels for use on macOS / iOS
// CBM mock peripherals (T.1 phase 1).
type BleInterface interface {
	ReadCmd() (bluetooth.Packet, error)
	StartMessageLoop()
	ReadMessage() (*message.Message, error)
	ReadMessageWithTimeout(time.Duration) (*message.Message, bool)
	WriteMessage(*message.Message)
	ShutdownConnection()
	RefreshAdvertisingWithSpecifiedId([]byte) error
	// Interrupt closes any in-flight ReadMessageWithTimeout so the pod
	// state machine's CommandLoop can bail out promptly on BLE disconnect
	// instead of waiting for the 60-second idle timeout. T.1 Phase 8.
	Interrupt()
}
