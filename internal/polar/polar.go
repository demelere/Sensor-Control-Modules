package polar

import (
	"encoding/binary"
	"fmt"

	"tinygo.org/x/bluetooth"
)

type PolarSensor struct {
	device       *bluetooth.Device
	heartRateCh  chan uint8
	rrIntervalCh chan []uint16
}

func newPolarSensor(adapter *bluetooth.Adapter, address bluetooth.Address) (*PolarSensor, error) { // TO DO: pass in mac address
	device, err := adapter.Connect(address, bluetooth.ConnectionParams{}) // TO DO: change to connect to specific mac address instead of by device name
	if err != nil {
		return nil, fmt.Errorf("failed to connect to Polar sensor: %v", err)
	}

	return &PolarSensor{
		device:       &device,
		heartRateCh:  make(chan uint8),
		rrIntervalCh: make(chan []uint16),
	}, nil
}

func (ps *PolarSensor) startPolarSensor() error {
	srvcs, err := ps.device.DiscoverServices([]bluetooth.UUID{bluetooth.ServiceUUIDHeartRate})
	if err != nil {
		return fmt.Errorf("failed to discover heart rate service: %v", err)
	}

	if len(srvcs) == 0 {
		return fmt.Errorf("could not find heart rate service")
	}

	srvc := srvcs[0]

	chars, err := srvc.DiscoverCharacteristics([]bluetooth.UUID{bluetooth.CharacteristicUUIDHeartRateMeasurement})
	if err != nil {
		return fmt.Errorf("failed to discover heart rate characteristic: %v", err)
	}

	if len(chars) == 0 {
		return fmt.Errorf("could not find heart rate characteristic")
	}

	char := chars[0]

	char.EnableNotifications(func(buf []byte) {
		if len(buf) > 1 {
			heartRate := buf[1]
			ps.heartRateCh <- heartRate

			flags := buf[0]
			if flags&0x10 != 0 && len(buf) >= 4 { // handle cases where RR interval data not available
				rrIntervals := make([]uint16, 0)
				for i := 2; i < len(buf); i += 2 { // avoiding panic situations by checking buffer lengths before accessing
					if i+1 < len(buf) {
						rrInterval := binary.LittleEndian.Uint16(buf[i:])
						rrIntervals = append(rrIntervals, rrInterval)
					}
				}
				ps.rrIntervalCh <- rrIntervals
			} else {
				ps.rrIntervalCh <- nil // send nil when RR interval data is not available
			}
		}
	})

	return nil
}

func (ps *PolarSensor) readHeartRate() uint8 {
	return <-ps.heartRateCh
}

func (ps *PolarSensor) readRRInterval() []uint16 {
	return <-ps.rrIntervalCh
}

func (ps *PolarSensor) close() error {
	return ps.device.Disconnect()
}
