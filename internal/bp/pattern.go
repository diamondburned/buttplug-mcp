package bp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/puzpuzpuz/xsync/v4"
)

// Pattern represents a vibration pattern.
type Pattern []PatternStep

// TotalDuration returns the total duration of the vibration pattern.
func (p Pattern) TotalDuration() time.Duration {
	var total time.Duration
	for _, step := range p {
		total += step.Duration()
	}
	return total
}

// PatternStep represents a single step in a vibration pattern.
type PatternStep struct {
	Strength    float64 `json:"strength"`
	DurationSec float64 `json:"duration_sec"`
}

// Duration returns the duration of the vibration pattern step.
func (p PatternStep) Duration() time.Duration {
	return time.Duration(p.DurationSec * float64(time.Second))
}

// PatternPlayer plays vibration patterns on devices.
// It wraps a [Manager] to manage pattern playback.
type PatternPlayer struct {
	bpm     *Manager
	logger  *slog.Logger
	devices *xsync.Map[int, *patternDeviceState]
	error   chan error // constant, used for surfacing errors
}

// NewPatternPlayer returns a new [PatternPlayer].
func NewPatternPlayer(bpm *Manager, logger *slog.Logger) *PatternPlayer {
	p := &PatternPlayer{
		bpm:     bpm,
		logger:  logger,
		devices: xsync.NewMap[int, *patternDeviceState](),
		error:   make(chan error, 1),
	}
	return p
}

// Play starts playing the given pattern on the given device ID. If a pattern
// is already playing on this device, it is stopped first.
func (p *PatternPlayer) Play(ctxCall context.Context, deviceID int, pattern Pattern, repeats int) error {
	done := make(chan struct{})
	ctxLoop, cancel := context.WithCancel(context.Background())

	s, _ := p.devices.LoadOrCompute(deviceID, func() (*patternDeviceState, bool) {
		return newPatternPlayerDeviceState(), false
	})
	select {
	case <-ctxCall.Done():
		return ctxCall.Err()
	case <-ctxLoop.Done():
		return ctxLoop.Err()
	case <-s.stopAndSwap(done, cancel):
		// continue
	}

	if err := p.popError(); err != nil {
		close(done)
		cancel()
		return err
	}

	go func() {
		defer close(done)
		defer cancel()

		logger := p.logger.With(
			"device_id", deviceID)
		logger.Debug(
			"starting pattern playback",
			"pattern_count", len(pattern),
			"pattern_repeats", repeats,
			"pattern_total_duration", pattern.TotalDuration())

		for repeat := 0; (repeats == 0) || (repeat < repeats) && ctxLoop.Err() == nil; repeat++ {
			for i, step := range pattern {
				logger.DebugContext(ctxLoop,
					"playing pattern step",
					"step", i,
					"step_strength", step.Strength,
					"step_duration", step.Duration(),
					"repeated", repeat)

				if err := p.bpm.DeviceVibrate(ctxLoop, deviceID, step.Strength); err != nil {
					if !errors.Is(err, context.Canceled) {
						logger.ErrorContext(ctxLoop,
							"failed to set device vibration strength, immediately stopping pattern playback",
							"step", i,
							"repeated", repeat,
							"error", err)

						// surface this error up.
						select {
						case p.error <- err:
						default:
						}
					}
					return
				}

				select {
				case <-ctxLoop.Done():
					return
				case <-time.After(step.Duration()):
				}
			}
		}

		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()

		if err := p.bpm.DeviceStop(ctx, deviceID); err != nil {
			logger.Error(
				"failed to stop device after pattern playback",
				"error", err)
		}
	}()

	return nil
}

// Stop stops any currently playing pattern, waiting for it to fully stop.
// It returns any error encountered during pattern playback.
func (p *PatternPlayer) Stop(deviceID int) error {
	device, ok := p.devices.LoadAndDelete(deviceID)
	if ok {
		<-device.stop()
	}
	return p.popError()
}

// StopAll stops all currently playing patterns. The background error is
// ignored.
func (p *PatternPlayer) StopAll() {
	var stops []<-chan struct{}
	// delete all devices in one go then wait for them to stop after:
	p.devices.Range(func(deviceID int, device *patternDeviceState) bool {
		p.devices.Delete(deviceID)
		stops = append(stops, device.stop())
		return true
	})
	for _, done := range stops {
		<-done
	}
}

func (p *PatternPlayer) popError() error {
	select {
	case err := <-p.error:
		return fmt.Errorf("previous playback resulted in error: %w", err)
	default:
		return nil
	}
}

type patternDeviceState struct {
	mu     sync.Mutex
	done   chan struct{}
	cancel func()
}

// newPatternPlayerDeviceState returns a new patternPlayerDeviceState.
// The state starts with the done channel immediately returning.
func newPatternPlayerDeviceState() *patternDeviceState {
	s := &patternDeviceState{cancel: func() {}}
	s.stop()
	return s
}

func (s *patternDeviceState) stop() (oldDone <-chan struct{}) {
	newDone := make(chan struct{})
	close(newDone)
	return s.stopAndSwap(newDone, func() {})
}

func (s *patternDeviceState) stopAndSwap(newDone chan struct{}, newCancel context.CancelFunc) (oldDone <-chan struct{}) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.cancel()
	oldDone = s.done

	s.done = newDone
	s.cancel = newCancel

	return oldDone
}
