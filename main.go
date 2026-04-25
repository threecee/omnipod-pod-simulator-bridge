package main

import (
	"flag"
	"fmt"
	"os"
	"sync"

	"github.com/avereha/pod/pkg/bluetooth"
	"github.com/avereha/pod/pkg/bridge"
	"github.com/avereha/pod/pkg/pod"

	"github.com/sirupsen/logrus"
	log "github.com/sirupsen/logrus"
)

// podAdapter glues bridge.Pod to pkg/pod by holding a *BridgeBle (which
// is the BleInterface the pod state machine talks to) and a *pod.Pod
// (whose StartAcceptingCommands runs in a goroutine, reading from the
// BridgeBle channels).
type podAdapter struct {
	p         *pod.Pod
	bridgeBle *bridge.BridgeBle
	out       *bridge.FrameWriter
	startOnce sync.Once
}

func (a *podAdapter) OnConnect() error {
	a.startOnce.Do(func() {
		// Drain BleCore's outbound CMD packets -> NOTIFY frames.
		go a.drain(bridge.CmdCharUUID, a.bridgeBle.PullCmd)
		// Drain BleCore's outbound DATA packets -> NOTIFY frames.
		go a.drain(bridge.DataCharUUID, a.bridgeBle.PullData)
		// Spin up the pod state machine. It blocks on bridgeBle channels.
		go func() {
			defer func() {
				if r := recover(); r != nil {
					log.Errorf("pod state machine panicked: %v", r)
				}
			}()
			a.p.StartAcceptingCommands()
		}()
	})
	return nil
}

func (a *podAdapter) OnDisconnect() error {
	// We don't tear down the pod state machine here — it's stateful and
	// the central may reconnect. The production *Ble had similar
	// semantics (StopMessageLoop was commented out on connect in the
	// original code).
	return nil
}

func (a *podAdapter) OnWrite(charUUID [16]byte, data []byte) ([]byte, error) {
	if !a.bridgeBle.RouteIncoming(charUUID, data) {
		return nil, fmt.Errorf("unknown characteristic UUID: %x", charUUID)
	}
	// Outbound NOTIFYs are emitted asynchronously by the drain goroutines
	// directly via a.out (the FrameWriter, which serializes frame-atomic
	// writes across all producers). Nothing synchronous to return here.
	return nil, nil
}

// drain pulls outbound packets and emits them as NOTIFY frames forever.
func (a *podAdapter) drain(charUUID [16]byte, pull func() bluetooth.Packet) {
	for {
		pkt := pull()
		notifyPayload := make([]byte, 16+len(pkt))
		copy(notifyPayload[:16], charUUID[:])
		copy(notifyPayload[16:], pkt)
		if err := a.out.WriteFrame(bridge.MsgNotify, notifyPayload); err != nil {
			log.Errorf("WriteFrame NOTIFY: %v", err)
			return
		}
	}
}

func main() {
	var stateFile = flag.String("state", "state.toml", "pod state file path")
	var freshState = flag.Bool("fresh", false, "start fresh (unactivated pod, empty state)")
	var noAutoDisconnect = flag.Bool("no-auto-disconnect", false, "disable Pi sim's auto-disconnect mimic (default off in tests)")
	var traceLevel = flag.Bool("v", false, "verbose logging (TraceLevel)")
	var infoLevel = flag.Bool("q", false, "quiet logging (InfoLevel only)")
	flag.Parse()

	if *traceLevel {
		log.SetLevel(log.TraceLevel)
	} else if *infoLevel {
		log.SetLevel(log.InfoLevel)
	} else {
		log.SetLevel(log.DebugLevel)
	}
	log.SetFormatter(&logrus.TextFormatter{DisableQuote: true, ForceColors: false})
	log.SetOutput(os.Stderr)

	if *noAutoDisconnect {
		// The original Pi sim's only auto-disconnect was the 1-minute
		// ReadMessageWithTimeout in pkg/pod/CommandLoop which triggers a
		// reconnect cycle. On the bridge transport this is now safe
		// (BridgeBle.ShutdownConnection stops the message loop so the
		// restart works), but the Swift side still controls when to
		// actually disconnect via CONNECT/DISCONNECT frames. The flag
		// is logged here so test harnesses know it was acknowledged;
		// it is currently informational.
		log.Info("auto-disconnect: bridge transport restarts cleanly on idle (controlled by Swift CONNECT/DISCONNECT frames)")
	}

	bridgeBle := bridge.NewBridgeBle()

	p := pod.New(bridgeBle, *stateFile, *freshState)

	out := bridge.NewFrameWriter(os.Stdout)
	adapter := &podAdapter{p: p, bridgeBle: bridgeBle, out: out}
	br := bridge.New(adapter)

	log.Info("pod-sim starting; reading frames from stdin")
	if err := br.Run(os.Stdin, out, os.Stderr); err != nil {
		fmt.Fprintf(os.Stderr, "bridge run error: %v\n", err)
		os.Exit(1)
	}
	log.Info("pod-sim exiting cleanly (stdin EOF)")
}
