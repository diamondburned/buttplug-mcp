// Copyright (c) 2025 Neomantra BV

// Package bp implements Buttplug.io device management.
package bp

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"sync"
	"time"

	"libdb.so/go-buttplug"
	buttplugschema "libdb.so/go-buttplug/schema/v3"
)

// Config conifgures Manager
type Config struct {
	// DebounceDuration determines the period of frequency to debounce certain commands
	// when sending them to the websocket. It works around the internal event
	// buffers for real-time control. Default is '50ms', 20Hz.   -1 to disable.
	DebounceDuration time.Duration // Duration for debounce (default is 50ms)
	// WebsocketHost is the host of the existing Intiface server to connect to.
	WebsocketHost string
	// WebsocketPort is the port of the existing Intiface server to connect to.
	WebsocketPort int
}

// Manager keeps track of Buttplug resources.
type Manager struct {
	config Config
	logger *slog.Logger
	conn   *buttplug.Websocket

	devicesMu sync.RWMutex
	devices   map[buttplugschema.DeviceIndex]KnownDevice
}

type KnownDevice struct {
	Name         buttplugschema.DeviceName
	Messages     buttplugschema.DeviceMessages
	BatteryLevel float64
}

// NewManager returns a new buttplug.Manager given a Config.
// Returns nil and an error if any.
func NewManager(config Config, logger *slog.Logger) (*Manager, error) {
	return &Manager{
		config: config,
		logger: logger,
		conn: buttplug.NewWebsocket(
			fmt.Sprintf("ws://%s:%d", config.WebsocketHost, config.WebsocketPort),
			&buttplug.WebsocketOpts{
				ServerName: "buttplug-mcp",
				Logger:     logger,
			},
		),
		devices: make(map[buttplugschema.DeviceIndex]KnownDevice),
	}, nil
}

///////////////////////////////////////////////////////////////////////////////

func (m *Manager) Run(ctx context.Context) error {
	msgs, cancel := m.conn.MessageChannel(ctx)
	defer cancel()

	connErr := make(chan error, 1)
	go func() { connErr <- m.conn.Start(ctx) }()

	for {
		select {
		case <-ctx.Done():
			return <-connErr // wait for connection to end

		case err := <-connErr:
			return fmt.Errorf("buttplug connection error: %w", err)

		case msg := <-msgs:
			switch msg := msg.(type) {
			case *buttplugschema.ServerInfoMessage:
				// Server is ready. Start scanning for all devices.
				m.conn.Send(ctx, &buttplugschema.StartScanningMessage{})
				m.conn.Send(ctx, &buttplugschema.RequestDeviceListMessage{})

			case *buttplugschema.DeviceListMessage:
				for _, device := range msg.Devices {
					m.logger.Info("listed device", "name", device.DeviceName, "index", device.DeviceIndex)
				}

			case *buttplugschema.DeviceAddedMessage:
				m.logger.Info("added device", "name", msg.DeviceName, "index", msg.DeviceIndex)

			case *buttplugschema.DeviceRemovedMessage:
				m.logger.Info("removed device", "index", msg.DeviceIndex)
			}

			m.handleDeviceMessage(msg)
		}
	}
}

func (m *Manager) handleDeviceMessage(msg buttplugschema.Message) {
	m.devicesMu.Lock()
	defer m.devicesMu.Unlock()

	switch msg := msg.(type) {
	case *buttplugschema.DeviceListMessage:
		clear(m.devices)
		for _, device := range msg.Devices {
			m.devices[device.DeviceIndex] = KnownDevice{
				Name:     device.DeviceName,
				Messages: device.DeviceMessages,
			}
		}
	case *buttplugschema.DeviceAddedMessage:
		m.devices[msg.DeviceIndex] = KnownDevice{
			Name:     msg.DeviceName,
			Messages: msg.DeviceMessages,
		}
	case *buttplugschema.DeviceRemovedMessage:
		delete(m.devices, msg.DeviceIndex)
	}
}

// Devices returns known device indexes.
func (m *Manager) DeviceIndexes() []buttplugschema.DeviceIndex {
	m.devicesMu.RLock()
	defer m.devicesMu.RUnlock()

	return slices.Collect(maps.Keys(m.devices))
}

// Device returns the known device at the given index.
func (m *Manager) Device(ctx context.Context, deviceIndex int) (*KnownDevice, error) {
	m.devicesMu.RLock()
	device, ok := m.devices[buttplugschema.DeviceIndex(deviceIndex)]
	m.devicesMu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("device %d not found", deviceIndex)
	}

	device.BatteryLevel = -1

	sensorIndex := slices.IndexFunc(device.Messages.SensorReadCmd, func(info buttplugschema.SensorReadCmdItem) bool {
		return info.SensorType == buttplug.SensorBattery
	})
	if sensorIndex == -1 {
		return &device, nil
	}

	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	reply, err := m.conn.SendCommand(ctx, &buttplugschema.SensorReadCmdMessage{
		DeviceIndex: buttplugschema.DeviceIndex(deviceIndex),
		SensorIndex: sensorIndex,
		SensorType:  buttplug.SensorBattery,
	})
	if err != nil {
		slog.Warn(
			"error querying battery",
			"deviceIndex", deviceIndex,
			"err", err)
	} else {
		reading, ok := reply.(*buttplugschema.SensorReadingMessage)
		if !ok {
			slog.Warn(
				"unexpected reply type from server when querying battery",
				"deviceIndex", deviceIndex,
				"replyType", fmt.Sprintf("%T", reply))
		} else {
			device.BatteryLevel = float64(reading.Data[0]) / 100
		}
	}

	return &device, nil
}

// DeviceVibrate vibrates the given device at the given strength (0.0 to 1.0).
func (m *Manager) DeviceVibrate(ctx context.Context, deviceIndex int, strength float64) error {
	m.devicesMu.RLock()
	device := m.devices[buttplugschema.DeviceIndex(deviceIndex)]
	m.devicesMu.RUnlock()

	// still unsure what the difference between scalar and linear cmds are.
	var scalars []buttplugschema.Scalar
	for i, info := range device.Messages.ScalarCmd {
		if info.ActuatorType != nil && *info.ActuatorType == buttplug.ActuatorVibrate {
			scalars = append(scalars, buttplugschema.Scalar{
				Index:        i,
				Scalar:       strength,
				ActuatorType: buttplug.ActuatorVibrate,
			})
		}
	}
	if len(scalars) == 0 {
		return fmt.Errorf("device %d has no vibrators", deviceIndex)
	}

	_, err := m.conn.Send(ctx, &buttplugschema.ScalarCmdMessage{
		DeviceIndex: buttplugschema.DeviceIndex(deviceIndex),
		Scalars:     scalars,
	})
	return err
}

// DeviceStop stops all actions on the given device.
func (m *Manager) DeviceStop(ctx context.Context, deviceIndex int) error {
	_, err := m.conn.Send(ctx, &buttplugschema.StopDeviceCmdMessage{
		DeviceIndex: buttplugschema.DeviceIndex(deviceIndex),
	})
	return err
}
