package transport

import (
	"sync"
	"testing"
	"time"
)

// fakeModem9704 models the two pins as the kits showed them on 21 Sep 2026:
// I_EN has a pull-up, so a line set and released (gpioSetValue) does not
// stay low, and only a held low shuts the modem down. I_BTD follows I_EN
// after a number of reads, since a real modem takes time to shut down and
// to boot. Every I_EN change that breaks the hardware guide's rule is
// recorded.
type fakeModem9704 struct {
	mu            sync.Mutex
	ien, btd      int
	held          bool
	shutdownReads int // reads of I_BTD before it drops after I_EN low
	bootReads     int // reads of I_BTD before it rises after I_EN high
	stuckHigh     bool
	pending       int
	sawShutdown   bool
	writes        int
	violations    []string
}

func (m *fakeModem9704) drive(v int) {
	if v == 1 && m.ien == 0 && m.btd == 1 && !m.stuckHigh {
		m.violations = append(m.violations, "I_EN high before I_BTD went low")
	}
	if v == 0 && m.ien == 1 && m.btd == 0 {
		m.violations = append(m.violations, "I_EN low before I_BTD went high")
	}
	if v != m.ien {
		m.ien = v
		if v == 0 {
			m.pending = m.shutdownReads
		} else {
			m.pending = m.bootReads
		}
	}
}

// set is gpioSetValue: the level lasts 100 ms, then the pull-up wins.
func (m *fakeModem9704) set(_ string, pin, v int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.writes++
	if pin != 26 {
		m.violations = append(m.violations, "drove a pin other than I_EN")
	}
	if v == 0 {
		m.violations = append(m.violations, "I_EN low without holding it: a 100 ms blip")
		return nil
	}
	m.drive(1)
	return nil
}

// hold is gpioHoldValue: the level stays until release, then the pull-up.
func (m *fakeModem9704) hold(_ string, pin, v int) (func(), error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.writes++
	if pin != 26 {
		m.violations = append(m.violations, "held a pin other than I_EN")
	}
	m.held = true
	m.drive(v)
	return func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		m.held = false
		m.drive(1)
	}, nil
}

func (m *fakeModem9704) get(_ string, pin int) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if pin != 23 {
		m.violations = append(m.violations, "read a pin other than I_BTD")
	}
	if m.btd != m.ien && !(m.stuckHigh && m.ien == 0) {
		if m.pending > 0 {
			m.pending--
		} else {
			m.btd = m.ien
			if m.btd == 0 {
				m.sawShutdown = true
			}
		}
	}
	return m.btd, nil
}

func withFakeModem(t *testing.T, m *fakeModem9704) *DirectIMTTransport {
	t.Helper()
	oldSet, oldHold, oldGet := imtGPIOSet, imtGPIOHold, imtGPIOGet
	oldShut, oldBoot, oldPoll, oldSettle := imtShutdownWait, imtBootWait, imtBTDPoll, imtBootedSettle
	imtGPIOSet, imtGPIOHold, imtGPIOGet = m.set, m.hold, m.get
	imtShutdownWait, imtBootWait, imtBTDPoll, imtBootedSettle = 300*time.Millisecond, 300*time.Millisecond, time.Millisecond, 0
	t.Cleanup(func() {
		imtGPIOSet, imtGPIOHold, imtGPIOGet = oldSet, oldHold, oldGet
		imtShutdownWait, imtBootWait, imtBTDPoll, imtBootedSettle = oldShut, oldBoot, oldPoll, oldSettle
	})
	tr := NewDirectIMTTransport("/dev/null")
	tr.SetGPIO("gpiochip4", 26, 23)
	return tr
}

// A power cycle really shuts the modem down (I_EN held low until I_BTD
// drops) and boots it again, each step on I_BTD, never on a fixed delay.
// [MESHSAT-1282]
func TestPowerCycleModem_ShutsDownAndBootsPerTheGuide(t *testing.T) {
	m := &fakeModem9704{ien: 1, btd: 1, shutdownReads: 20, bootReads: 20}
	withFakeModem(t, m).powerCycleModem()
	if len(m.violations) > 0 {
		t.Fatalf("power sequence broken: %v", m.violations)
	}
	if !m.sawShutdown {
		t.Fatal("the modem never shut down")
	}
	if m.ien != 1 || m.btd != 1 || m.held {
		t.Fatalf("modem left ien=%d btd=%d held=%v, want booted and released", m.ien, m.btd, m.held)
	}
}

// A modem still booting (I_BTD low, I_EN high) is not switched off under
// itself; the boot it is doing is the fresh start.
func TestPowerCycleModem_ModemStillBootingIsNotSwitchedOff(t *testing.T) {
	m := &fakeModem9704{ien: 1, btd: 0, bootReads: 5, pending: 5}
	withFakeModem(t, m).powerCycleModem()
	if len(m.violations) > 0 {
		t.Fatalf("power sequence broken: %v", m.violations)
	}
	if m.sawShutdown || m.btd != 1 {
		t.Fatalf("sawShutdown=%v btd=%d, want no shutdown and a finished boot", m.sawShutdown, m.btd)
	}
}

// If I_BTD never drops, the modem is still switched back on, after the full
// wait, and the line is released.
func TestPowerCycleModem_StuckBTDStillEndsWithTheModemOn(t *testing.T) {
	m := &fakeModem9704{ien: 1, btd: 1, stuckHigh: true}
	tr := withFakeModem(t, m)
	start := time.Now()
	tr.powerCycleModem()
	if waited := time.Since(start); waited < imtShutdownWait {
		t.Fatalf("switched back on after %s, before the %s shutdown wait", waited, imtShutdownWait)
	}
	if m.ien != 1 || m.held {
		t.Fatalf("modem left ien=%d held=%v", m.ien, m.held)
	}
}

// Startup is Ground Control's rbBeginGpio: a booted modem is left alone.
func TestEnsureModemOn_BootedModemIsLeftAlone(t *testing.T) {
	m := &fakeModem9704{ien: 1, btd: 1}
	withFakeModem(t, m).ensureModemOn()
	if m.writes != 0 || len(m.violations) > 0 {
		t.Fatalf("%d I_EN writes on a booted modem (violations %v), want none", m.writes, m.violations)
	}
}

// A modem that is off is switched on and waited for.
func TestEnsureModemOn_ModemOffIsBooted(t *testing.T) {
	m := &fakeModem9704{ien: 0, btd: 0, bootReads: 5}
	withFakeModem(t, m).ensureModemOn()
	if len(m.violations) > 0 || m.ien != 1 || m.btd != 1 {
		t.Fatalf("ien=%d btd=%d violations=%v, want a booted modem", m.ien, m.btd, m.violations)
	}
}
