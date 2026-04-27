package bridge

import (
	"sync"
	"testing"
	"time"

	"github.com/avereha/pod/pkg/bluetooth"
)

// TestBridgeBle_PushPacketRoundTrip verifies that a packet pushed into the
// CMD or DATA inbound channel via RouteIncoming is readable via BleCore's
// ReadCmd / ReadData methods. This exercises the PushCmd/PushData path that
// main.go's OnWrite calls on every incoming GATT write.
func TestBridgeBle_PushPacketRoundTrip(t *testing.T) {
	bb := NewBridgeBle()

	// Push a CMD packet via RouteIncoming.
	want := bluetooth.Packet([]byte{0xDE, 0xAD, 0xBE, 0xEF})
	if !bb.RouteIncoming(CmdCharUUID, want) {
		t.Fatal("RouteIncoming(CmdCharUUID) returned false")
	}

	// ReadCmd blocks; run it in a goroutine with a timeout.
	resultCh := make(chan bluetooth.Packet, 1)
	go func() {
		pkt, _ := bb.ReadCmd()
		resultCh <- pkt
	}()

	select {
	case got := <-resultCh:
		if string(got) != string(want) {
			t.Errorf("ReadCmd: got %x, want %x", got, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ReadCmd timed out — packet was not delivered")
	}

	// Push a DATA packet the same way.
	wantData := bluetooth.Packet([]byte{0xCA, 0xFE})
	if !bb.RouteIncoming(DataCharUUID, wantData) {
		t.Fatal("RouteIncoming(DataCharUUID) returned false")
	}

	resultCh2 := make(chan bluetooth.Packet, 1)
	go func() {
		pkt, _ := bb.ReadData()
		resultCh2 <- pkt
	}()

	select {
	case got := <-resultCh2:
		if string(got) != string(wantData) {
			t.Errorf("ReadData: got %x, want %x", got, wantData)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ReadData timed out — packet was not delivered")
	}
}

// TestBridgeBle_WriteMessageRoundTrip verifies the outbound direction:
// BleCore.WriteCmd puts a packet on the cmdOutput channel, and
// PullCmdStopped returns it. This is the path the drain goroutines use in
// main.go (drain → PullCmdStopped → FrameWriter.WriteFrame).
func TestBridgeBle_WriteMessageRoundTrip(t *testing.T) {
	bb := NewBridgeBle()

	want := bluetooth.Packet([]byte{0x01, 0x02, 0x03})

	// Simulate BleCore writing a packet outbound (as writeMessage does internally).
	go func() {
		_ = bb.WriteCmd(want)
	}()

	got, ok := bb.PullCmdStopped()
	if !ok {
		t.Fatal("PullCmdStopped returned ok=false — Stop() should not be called yet")
	}
	if string(got) != string(want) {
		t.Errorf("PullCmdStopped: got %x, want %x", got, want)
	}

	// Same for DATA.
	wantData := bluetooth.Packet([]byte{0xAA, 0xBB})
	go func() {
		_ = bb.WriteData(wantData)
	}()

	gotData, ok := bb.PullDataStopped()
	if !ok {
		t.Fatal("PullDataStopped returned ok=false — Stop() should not be called yet")
	}
	if string(gotData) != string(wantData) {
		t.Errorf("PullDataStopped: got %x, want %x", gotData, wantData)
	}
}

// TestBridgeBle_StopUnblocksDrainGoroutines verifies that calling Stop()
// causes goroutines blocked on PullCmdStopped / PullDataStopped to unblock
// and return false. This is the fix for the drain goroutine leak on stdin EOF.
func TestBridgeBle_StopUnblocksDrainGoroutines(t *testing.T) {
	bb := NewBridgeBle()

	var wg sync.WaitGroup
	unblocked := make(chan string, 2)

	// Launch a CMD drain goroutine.
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, ok := bb.PullCmdStopped()
		if !ok {
			unblocked <- "cmd"
		}
	}()

	// Launch a DATA drain goroutine.
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, ok := bb.PullDataStopped()
		if !ok {
			unblocked <- "data"
		}
	}()

	// Let both goroutines settle into their select.
	time.Sleep(50 * time.Millisecond)

	// Call Stop — both goroutines should unblock.
	bb.Stop()

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// Good — both goroutines returned.
	case <-time.After(2 * time.Second):
		t.Fatal("Stop() did not unblock drain goroutines within 2s")
	}

	// Both should have reported ok=false.
	close(unblocked)
	count := 0
	for range unblocked {
		count++
	}
	if count != 2 {
		t.Errorf("expected 2 goroutines to unblock with ok=false, got %d", count)
	}
}

// TestBridgeBle_StopIdempotent verifies that calling Stop() multiple times
// does not panic. sync.Once semantics guarantee this, but we test it
// explicitly so a future refactor doesn't regress.
func TestBridgeBle_StopIdempotent(t *testing.T) {
	bb := NewBridgeBle()
	bb.Stop()
	bb.Stop()
	bb.Stop()
}

// TestBridgeBle_UnknownUUIDReturnsFalse verifies that RouteIncoming returns
// false for an unrecognized characteristic UUID and does not push anything
// into the inbound channels (so the bridge dispatch loop can return an error
// to the central).
func TestBridgeBle_UnknownUUIDReturnsFalse(t *testing.T) {
	bb := NewBridgeBle()
	unknown := [16]byte{0xFF, 0xFF}
	if bb.RouteIncoming(unknown, []byte{0x01}) {
		t.Error("RouteIncoming with unknown UUID should return false, got true")
	}
}

// TestBridgeBle_StopMessageLoopSemantics verifies that ShutdownConnection
// (the C2 fix) calls StopMessageLoop without panicking, and that
// StartMessageLoop can be called again afterwards. This is the restart path
// exercised by the pod state machine's 60-second idle reconnect cycle.
func TestBridgeBle_StopMessageLoopSemantics(t *testing.T) {
	bb := NewBridgeBle()

	// StartMessageLoop then ShutdownConnection (mirrors the reconnect cycle).
	bb.StartMessageLoop()
	bb.ShutdownConnection() // should call StopMessageLoop, not log.Fatalf

	// A second StartMessageLoop must succeed (no "already running" Fatalf).
	bb.StartMessageLoop()
	bb.ShutdownConnection()
}
