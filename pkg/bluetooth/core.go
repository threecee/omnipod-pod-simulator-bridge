// BleCore — the cross-platform GATT-fragment-protocol assembly logic.
//
// This file holds the channel-based message loop that turns raw fragmented
// GATT packets (CMD + DATA characteristics) into pkg/message.Message and
// vice-versa. The Linux production *Ble (bluetooth.go, build-tagged) embeds
// *BleCore and adds the paypal/gatt peripheral layer on top. The stdio
// bridge (pkg/bridge.BridgeBle) also embeds *BleCore and feeds it from
// stdin frames instead of real BLE.

package bluetooth

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"hash/crc32"
	"sync"
	"time"

	"github.com/avereha/pod/pkg/message"
	"github.com/davecgh/go-spew/spew"
	log "github.com/sirupsen/logrus"
)

// BleCore owns the channels that the protocol loop reads/writes. Construct
// it via NewBleCore(). The production *Ble and the stdio BridgeBle both
// embed *BleCore.
type BleCore struct {
	cmdInput   chan Packet
	dataInput  chan Packet
	cmdOutput  chan Packet
	dataOutput chan Packet

	messageInput  chan *message.Message
	messageOutput chan *message.Message

	stopLoop chan bool
	stopMtx  sync.Mutex
}

// NewBleCore returns a freshly-buffered BleCore with channels matching the
// original *Ble buffering (5 for raw packets, 5/2 for messages).
func NewBleCore() *BleCore {
	return &BleCore{
		cmdInput:      make(chan Packet, 5),
		dataInput:     make(chan Packet, 5),
		cmdOutput:     make(chan Packet, 5),
		dataOutput:    make(chan Packet, 5),
		messageInput:  make(chan *message.Message, 5),
		messageOutput: make(chan *message.Message, 2),
	}
}

// PushCmd / PushData are how a transport (real BLE or stdio bridge) feeds
// raw incoming GATT packets into the loop.
func (b *BleCore) PushCmd(p Packet)  { b.cmdInput <- p }
func (b *BleCore) PushData(p Packet) { b.dataInput <- p }

// PullCmd / PullData are how a transport drains outgoing GATT packets to
// notify the central. These block; transports should run them in a goroutine.
func (b *BleCore) PullCmd() Packet  { return <-b.cmdOutput }
func (b *BleCore) PullData() Packet { return <-b.dataOutput }

// PullCmdWithTimeout / PullDataWithTimeout — non-blocking variants for
// transports that want to multiplex in a single goroutine (e.g., the stdio
// bridge's drain loop).
func (b *BleCore) PullCmdWithTimeout(d time.Duration) (Packet, bool) {
	select {
	case p := <-b.cmdOutput:
		return p, true
	case <-time.After(d):
		return nil, false
	}
}

func (b *BleCore) PullDataWithTimeout(d time.Duration) (Packet, bool) {
	select {
	case p := <-b.dataOutput:
		return p, true
	case <-time.After(d):
		return nil, false
	}
}

// --- BleInterface methods ---

func (b *BleCore) WriteCmd(packet Packet) error {
	b.cmdOutput <- packet
	return nil
}

func (b *BleCore) WriteData(packet Packet) error {
	b.dataOutput <- packet
	return nil
}

func (b *BleCore) writeDataBuffer(buf *bytes.Buffer) error {
	data := make([]byte, buf.Len())
	copy(data, buf.Bytes())
	buf.Reset()
	return b.WriteData(data)
}

func (b *BleCore) ReadCmd() (Packet, error) {
	packet := <-b.cmdInput
	return packet, nil
}

func (b *BleCore) ReadData() (Packet, error) {
	packet := <-b.dataInput
	return packet, nil
}

func (b *BleCore) ReadMessage() (*message.Message, error) {
	m := <-b.messageInput
	return m, nil
}

func (b *BleCore) ReadMessageWithTimeout(d time.Duration) (*message.Message, bool) {
	select {
	case m := <-b.messageInput:
		return m, false
	case <-time.After(d):
		log.Debugf("ReadMessage timeout")
		return nil, true
	}
}

func (b *BleCore) WriteMessage(m *message.Message) {
	b.messageOutput <- m
}

func (b *BleCore) loop(stop chan bool) {
	for {
		select {
		case <-stop:
			return
		case msg := <-b.messageOutput:
			b.writeMessage(msg)
		case cmd := <-b.cmdInput:
			msg, err := b.readMessage(cmd)
			if err != nil {
				log.Fatalf("pkg bluetooth; error reading message: %s", err)
			}
			b.messageInput <- msg
		}
	}
}

func (b *BleCore) StartMessageLoop() {
	b.stopMtx.Lock()
	defer b.stopMtx.Unlock()
	if b.stopLoop != nil {
		log.Fatalf("pkg bluetooth; Messaging loop is already running")
	}
	b.stopLoop = make(chan bool)
	go b.loop(b.stopLoop)
}

func (b *BleCore) StopMessageLoop() {
	b.stopMtx.Lock()
	defer b.stopMtx.Unlock()
	if b.stopLoop != nil {
		close(b.stopLoop)
		b.stopLoop = nil
	}
}

func (b *BleCore) expectCommand(expected Packet) {
	cmd, _ := b.ReadCmd()
	if !bytes.Equal(expected[:1], cmd[:1]) {
		log.Fatalf("pkg bluetooth; expected command: %s. received command: %s", expected, cmd)
	}
}

func (b *BleCore) writeMessage(msg *message.Message) {
	var buf bytes.Buffer
	var index byte = 0

	b.WriteCmd(CmdRTS)
	b.expectCommand(CmdCTS) // TODO figure out what to do if !CTS
	bytes, err := msg.Marshal()
	if err != nil {
		log.Fatalf("pkg bluetooth; could not marshal the message %s", err)
	}
	log.Tracef("pkg bluetooth; Sending message: %x", bytes)
	sum := crc32.ChecksumIEEE(bytes)
	if len(bytes) <= 18 {
		buf.WriteByte(index) // index
		buf.WriteByte(0)     // fragments

		buf.WriteByte(byte(sum >> 24))
		buf.WriteByte(byte(sum >> 16))
		buf.WriteByte(byte(sum >> 8))
		buf.WriteByte(byte(sum))
		buf.WriteByte((byte(len(bytes))))
		end := len(bytes)
		if len(bytes) > 14 {
			end = 14
		}
		buf.Write(bytes[:end])
		b.writeDataBuffer(&buf)

		if len(bytes) > 14 {
			buf.WriteByte(index)
			buf.WriteByte(byte(len(bytes) - 14))
			buf.Write(bytes[14:])
			b.writeDataBuffer(&buf)
		}
		return
	}

	size := len(bytes)
	fullFragments := (byte)((size - 18) / 19)
	rest := (byte)((size - (int(fullFragments) * 19)) - 18)
	buf.WriteByte(index)
	buf.WriteByte(fullFragments + 1)
	buf.Write(bytes[:18])

	b.writeDataBuffer(&buf)

	for index = 1; index <= fullFragments; index++ {
		buf.WriteByte(index)
		if index == 1 {
			buf.Write(bytes[18:37])
		} else {
			buf.Write(bytes[(index-1)*19+18 : (index-1)*19+18+19])
		}
		b.writeDataBuffer(&buf)
	}

	buf.WriteByte(index)
	buf.WriteByte(rest)
	buf.WriteByte(byte(sum >> 24))
	buf.WriteByte(byte(sum >> 16))
	buf.WriteByte(byte(sum >> 8))
	buf.WriteByte(byte(sum))
	end := rest
	if rest > 14 {
		end = 14
	}
	buf.Write(bytes[(fullFragments*19)+18 : (fullFragments*19)+18+end])
	b.writeDataBuffer(&buf)
	if rest > 14 {
		index++
		buf.WriteByte(index)
		buf.WriteByte(rest - 14)
		buf.Write(bytes[fullFragments*19+18+14:])
		for buf.Len() < 20 {
			buf.WriteByte(0)
		}
		b.writeDataBuffer(&buf)
	}
	b.expectCommand(CmdSuccess)
}

func (b *BleCore) readMessage(cmd Packet) (*message.Message, error) {
	var buf bytes.Buffer
	var checksum []byte

	log.Trace("pkg bluetooth; Reading RTS")
	if !bytes.Equal(CmdRTS[:1], cmd[:1]) {
		log.Fatalf("pkg bluetooth; expected command: %x. received command: %x", CmdRTS, cmd)
	}
	log.Trace("pkg bluetooth; Sending CTS")

	b.WriteCmd(CmdCTS)

	first, _ := b.ReadData()
	fragments := int(first[1])
	expectedIndex := 1
	oneExtra := false
	if fragments == 0 {
		checksum = first[2:6]
		len := first[6]
		end := len + 7
		if len > 13 {
			oneExtra = true
			end = 20
		}
		buf.Write(first[7:end])
	} else {
		buf.Write(first[2:20])
	}
	for i := 1; i < fragments; i++ {
		data, _ := b.ReadData()
		if i == expectedIndex {
			buf.Write(data[1:20])
		} else {
			log.Warnf("pkg bluetooth; sending NACK, packet index is wrong")
			buf.Write(data[:])
			CmdNACK[1] = byte(expectedIndex)
			b.WriteCmd(CmdNACK)
		}
		expectedIndex++
	}
	if fragments != 0 {
		data, _ := b.ReadData()
		l := data[1]
		if l > 14 {
			oneExtra = true
			l = 14
		}
		checksum = data[2:6]
		buf.Write(data[6 : l+6])
	}
	log.Tracef("pkg bluetooth; One extra: %t", oneExtra)
	if oneExtra {
		data, _ := b.ReadData()
		buf.Write(data[2 : data[1]+2])
	}
	bts := buf.Bytes()
	sum := crc32.ChecksumIEEE(bts)
	if binary.BigEndian.Uint32(checksum) != sum {
		log.Warnf("pkg bluetooth; checksum missmatch. checksum is: %x. want: %x", sum, checksum)
		log.Warnf("pkg bluetooth; data: %s", hex.EncodeToString(bts))

		b.WriteCmd(CmdFail)
		return nil, errors.New("checksum missmatch")
	}

	b.WriteCmd(CmdSuccess)

	msg, _err := message.Unmarshal(bts)
	log.Tracef("pkg bluetooth; Received message: %s", spew.Sdump(msg))

	return msg, _err
}
