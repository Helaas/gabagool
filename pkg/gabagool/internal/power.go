package internal

import (
	"log"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"github.com/holoplot/go-evdev"
)

type PowerButtonConfig struct {
	ButtonCode    int
	ButtonCodes   []int
	DevicePath    string
	ShortPressMax time.Duration
	CoolDownTime  time.Duration
	SuspendScript string
}

func (c PowerButtonConfig) matchesButton(code evdev.EvCode) bool {
	if code == evdev.EvCode(c.ButtonCode) {
		return true
	}
	for _, buttonCode := range c.ButtonCodes {
		if code == evdev.EvCode(buttonCode) {
			return true
		}
	}
	return false
}

type stoppableTimer interface {
	Stop() bool
}

type powerButtonState struct {
	config        PowerButtonConfig
	pressTime     time.Time
	holdTimer     stoppableTimer
	cooldownUntil time.Time

	now       func() time.Time
	afterFunc func(time.Duration, func()) stoppableTimer
	suspend   func()
	shutdown  func()
}

func newPowerButtonState(config PowerButtonConfig) *powerButtonState {
	return &powerButtonState{
		config:    config,
		now:       time.Now,
		afterFunc: func(d time.Duration, f func()) stoppableTimer { return time.AfterFunc(d, f) },
		suspend:   func() { runScript(config.SuspendScript) },
		shutdown:  signalPoweroffAndExit,
	}
}

func (s *powerButtonState) handleValue(value int32) {
	if s.now().Before(s.cooldownUntil) {
		return
	}

	switch value {
	case 1:
		s.pressTime = s.now()
		if s.holdTimer != nil {
			s.holdTimer.Stop()
		}
		s.holdTimer = s.afterFunc(s.config.ShortPressMax, func() {
			log.Println("Button held for 2 seconds, signaling shutdown...")
			s.shutdown()
		})
	case 0:
		if s.pressTime.IsZero() {
			return
		}

		s.pressTime = time.Time{}
		stopped := false
		if s.holdTimer != nil {
			stopped = s.holdTimer.Stop()
			s.holdTimer = nil
		}

		if stopped {
			log.Println("Short press detected, suspending...")
			s.suspend()
			s.cooldownUntil = s.now().Add(s.config.CoolDownTime)
		}
	}
}

// Adapted from https://github.com/ben16w/minui-power-control
func PowerButtonHandler(wg *sync.WaitGroup, config PowerButtonConfig) {
	defer wg.Done()

	dev, err := evdev.Open(config.DevicePath)
	if err != nil {
		log.Fatalf("Failed to open input device: %v", err)
	}
	log.Printf("Listening on device: %s\n", config.DevicePath)

	state := newPowerButtonState(config)

	for {
		event, err := dev.ReadOne()
		if err != nil {
			log.Printf("Failed to read input: %v", err)
			continue
		}

		if event.Type == evdev.EV_KEY && config.matchesButton(event.Code) {
			state.handleValue(event.Value)
		}
	}
}

func runScript(scriptPath string) {
	cmd := exec.Command(scriptPath)
	if err := cmd.Run(); err != nil {
		log.Printf("Failed to run %s script: %v", scriptPath, err)
	}
}

// signalPoweroffAndExit creates /tmp/poweroff and exits the process.
// NextUI's MinUI.pak/launch.sh detects this file after the pak exits and
// calls poweroff_next, replacing the previous direct /sbin/poweroff call.
func signalPoweroffAndExit() {
	f, err := os.Create("/tmp/poweroff")
	if err != nil {
		log.Printf("Failed to touch /tmp/poweroff: %v", err)
	} else {
		_ = f.Close()
	}
	syscall.Sync()
	os.Exit(0)
}
