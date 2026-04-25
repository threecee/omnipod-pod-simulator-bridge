//go:build linux
// +build linux

package bluetooth

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"sync"

	"github.com/paypal/gatt"
	"github.com/paypal/gatt/linux/cmd"
	log "github.com/sirupsen/logrus"
)

type Ble struct {
	*BleCore

	device  *gatt.Device
	central *gatt.Central

	cmdNotifier    gatt.Notifier
	cmdNotifierMtx sync.Mutex

	dataNotifier    gatt.Notifier
	dataNotifierMtx sync.Mutex
}

var DefaultServerOptions = []gatt.Option{
	gatt.LnxMaxConnections(1),
	gatt.LnxDeviceID(-1, true),
	gatt.LnxSetAdvertisingParameters(&cmd.LESetAdvertisingParameters{
		AdvertisingIntervalMin: 0x00f4,
		AdvertisingIntervalMax: 0x00f4,
		AdvertisingChannelMap:  0x7,
	}),
}

func New(adapterID string, podId []byte) (*Ble, error) {
	d, err := gatt.NewDevice(DefaultServerOptions...)
	if err != nil {
		log.Fatalf("pkg bluetooth; failed to open device, err: %s", err)
	}

	b := &Ble{
		BleCore: NewBleCore(),
		device:  &d,
	}

	d.Handle(
		gatt.CentralConnected(func(c gatt.Central) {
			fmt.Println("pkg bluetooth; ** New connection from: ", c.ID())
			// b.StopMessageLoop()
			b.central = &c
		}),
		gatt.CentralDisconnected(func(c gatt.Central) {
			log.Tracef("pkg bluetooth; ** disconnect: %s", c.ID())
		}),
	)

	// Start cmd writing goroutine — drains BleCore.cmdOutput and pushes
	// notifications out through the GATT cmd characteristic notifier.
	go func() {
		for {
			packet := b.PullCmd()
			b.cmdNotifierMtx.Lock()
			if b.cmdNotifier.Done() {
				log.Fatalf("pkg bluetooth; CMD closed")
			}
			ret, err := b.cmdNotifier.Write(packet)
			b.cmdNotifierMtx.Unlock()
			log.Tracef("pkg bluetooth; CMD notification return: %d/%s", ret, hex.EncodeToString(packet))
			if err != nil {
				log.Fatalf("pkg bluetooth; error writing CMD: %s", err)
			}
		}
	}()

	// Start data writing goroutine
	go func() {
		for {
			packet := b.PullData()
			b.dataNotifierMtx.Lock()
			if b.dataNotifier.Done() {
				log.Fatalf("pkg bluetooth; DATA closed")
			}
			ret, err := b.dataNotifier.Write(packet)
			b.dataNotifierMtx.Unlock()
			log.Tracef("pkg bluetooth; DATA notification return: %d/%s", ret, hex.EncodeToString(packet))
			if err != nil {
				log.Fatalf("pkg bluetooth; error writing DATA: %s ", err)
			}
		}
	}()

	// A mandatory handler for monitoring device state.
	onStateChanged := func(d gatt.Device, s gatt.State) {
		fmt.Printf("state: %s\n", s)
		switch s {
		case gatt.StatePoweredOn:
			var serviceUUID = gatt.MustParseUUID("1a7e-4024-e3ed-4464-8b7e-751e03d0dc5f")
			var cmdCharUUID = gatt.MustParseUUID("1a7e-2441-e3ed-4464-8b7e-751e03d0dc5f")
			var dataCharUUID = gatt.MustParseUUID("1a7e-2442-e3ed-4464-8b7e-751e03d0dc5f")

			s := gatt.NewService(serviceUUID)

			cmdCharacteristic := s.AddCharacteristic(cmdCharUUID)
			cmdCharacteristic.HandleWriteFunc(
				func(r gatt.Request, data []byte) (status byte) {
					log.Tracef("received CMD,  %x", data)
					ret := make([]byte, len(data))
					copy(ret, data)
					b.PushCmd(Packet(ret))
					return 0
				})

			cmdCharacteristic.HandleNotifyFunc(
				func(r gatt.Request, n gatt.Notifier) {
					b.cmdNotifierMtx.Lock()
					b.cmdNotifier = n
					b.cmdNotifierMtx.Unlock()
					log.Infof("pkg bluetooth; handling CMD notifications on new connection from:  %s", r.Central.ID())
				})

			dataCharacteristic := s.AddCharacteristic(dataCharUUID)
			dataCharacteristic.HandleNotifyFunc(
				func(r gatt.Request, n gatt.Notifier) {
					b.dataNotifierMtx.Lock()
					b.dataNotifier = n
					b.dataNotifierMtx.Unlock()
					log.Infof("pkg bluetooth; handling DATA notifications on new connection from: %s", r.Central.ID())
					log.Infof("     *** OK to send commands from the phone app ***")
				})

			dataCharacteristic.HandleWriteFunc(
				func(r gatt.Request, data []byte) (status byte) {
					log.Tracef("pkg bluetooth; received DATA,%x, -- %d", data, len(data))
					ret := make([]byte, len(data))
					copy(ret, data)
					b.PushData(Packet(ret))
					return 0
				})

			err = d.AddService(s)
			if err != nil {
				log.Fatalf("pkg bluetooth; could not add service: %s", err)
			}

			podIdServiceOne := gatt.UUID16(0xffff)
			podIdServiceTwo := gatt.UUID16(0xfffe)
			if podId != nil {
				podIdServiceOne = gatt.UUID16(binary.BigEndian.Uint16(podId[0:2]))
				podIdServiceTwo = gatt.UUID16(binary.BigEndian.Uint16(podId[2:4]))
			}

			// Advertise device name and service's UUIDs.
			err = d.AdvertiseNameAndServices(" :: Fake POD ::", []gatt.UUID{
				gatt.UUID16(0x4024),

				gatt.UUID16(0x2470),
				gatt.UUID16(0x000a),

				podIdServiceOne,
				podIdServiceTwo,

				// these 4 are copied from lotNo and lotSeq from fixed string in versionresponse.go
				gatt.UUID16(0x0814),
				gatt.UUID16(0x6DB1),
				gatt.UUID16(0x0006),
				gatt.UUID16(0xE451),
			})
			if err != nil {
				log.Fatalf("pkg bluetooth; could not advertise: %s", err)
			}
		default:
		}
	}
	err = d.Init(onStateChanged)
	if err != nil {
		log.Fatalf("pkg bluetooth; could not init bluetooth: %s", err)
	}
	return b, nil
}

func (b *Ble) RefreshAdvertisingWithSpecifiedId(id []byte) error { // 4 bytes, first 2 usually empty
	log.Debugf("RefreshAdvertisingWithSpecifiedId %x", id)
	// Looking at the paypal/gatt source code, we don't need to call StopAdvertising,
	// but just call AdvertiseNameAndServices and it should update

	log.Tracef("podIdServiceOne %v", gatt.UUID16(binary.BigEndian.Uint16(id[0:2])))
	log.Tracef("podIdServiceTwo %v", gatt.UUID16(binary.BigEndian.Uint16(id[2:4])))
	err := (*b.device).AdvertiseNameAndServices(" :: Fake POD ::", []gatt.UUID{
		gatt.UUID16(0x4024),

		gatt.UUID16(0x2470),
		gatt.UUID16(0x000a),

		gatt.UUID16(binary.BigEndian.Uint16(id[0:2])),
		gatt.UUID16(binary.BigEndian.Uint16(id[2:4])),

		// these 4 are copied from lotNo and lotSeq from fixed string in versionresponse.go
		gatt.UUID16(0x0814),
		gatt.UUID16(0x6DB1),
		gatt.UUID16(0x0006),
		gatt.UUID16(0xE451),
	})
	if err != nil {
		log.Infof("pkg bluetooth; could not re-advertise: %s", err)
	}
	return err
}

func (b *Ble) ShutdownConnection() {
	(*b.central).Close()
}
