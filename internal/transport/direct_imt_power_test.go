package transport

import (
	"sync"
	"testing"
	"time"
)

// fakeModem9704 models the two pins: I_BTD follows I_EN after a number of
// reads, as a real modem takes time to shut down and to boot. It records
// every I_EN edge that breaks the hardware guide's rule.
type fakeModem9704 struct {
	mu            sync.Mutex
	ien, btd      int
	shutdownReads int // reads of I_BTD before it drops after I_EN low
	bootReads     int // reads of I_BTD before it rises after I_EN high
	stuckHigh     bool
	pending       int
	edges         []int
	violations    []string
}

func (m *fakeModem9704) set(_ string, pin, v int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if pin != 26 {
		m.violations = append(m.violations, "drove a pin other than I_EN")
		return nil
	}
	if v == 1 && m.ien == 0 && m.btd == 1 {
		m.violations = append(m.violations, "I_EN high before I_BTD went low")
	}
	if v == 0 && m.ien == 1 && m.btd == 0 {
		m.violations = append(m.violations, "I_EN low before I_BTD went high")
	}
	if v != m.ien {
		m.ien = v
		m.edges = append(m.edges, v)
		if v == 0 {
			m.pending = m.shutdownReads
		} else {
			m.pending = m.bootReads
		}
	}
	return nil
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
		}
	}
	return m.btd, nil
}

func withFakeModem(t *testing.T, m *fakeModem9704) *DirectIMTTransport {
	t.Helper()
	oldSet, oldGet := imtGPIOSet, imtGPIOGet
	oldShut, oldBoot, oldPoll, oldSettle := imtShutdownWait, imtBootWait, imtBTDPoll, imtBootedSettle
	imtGPIOSet, imtGPIOGet = m.set, m.get
	imtShutdownWait, imtBootWait, imtBTDPoll, imtBootedSettle = 300*time.Millisecond, 300*time.Millisecond, time.Millisecond, 0
	t.Cleanup(func() {
		imtGPIOSet, imtGPIOGet = oldSet, oldGet
		imtShutdownWait, imtBootWait, imtBTDPoll, imtBootedSettle = oldShut, oldBoot, oldPoll, oldSettle
	})
	tr := NewDirectIMTTransport("/dev/null")
	tr.SetGPIO("gpiochip4", 26, 23)
	return tr
}

// A booted modem is shut down and booted again, each step waiting for
// I_BTD, never on a fixed delay. [MESHSAT-1282]
func TestResetModemJSPR_FollowsTheGuidesPowerSequence(t *testing.T) {
	m := &fakeModem9704{ien: 1, btd: 1, shutdownReads: 20, bootReads: 20}
	withFakeModem(t, m).resetModemJSPR()
	if len(m.violations) > 0 {
		t.Fatalf("power sequence broken: %v", m.violations)
	}
	if len(m.edges) != 2 || m.edges[0] != 0 || m.edges[1] != 1 {
		t.Fatalf("I_EN edges %v, want low then high", m.edges)
	}
	if m.ien != 1 || m.btd != 1 {
		t.Fatalf("modem left ien=%d btd=%d, want booted", m.ien, m.btd)
	}
}

// A modem that is still booting (I_BTD low, I_EN high) must not have I_EN
// pulled low under it; the boot it is doing is the fresh start.
func TestResetModemJSPR_ModemStillBootingIsNotSwitchedOff(t *testing.T) {
	m := &fakeModem9704{ien: 1, btd: 0, bootReads: 5}
	m.pending = 5
	withFakeModem(t, m).resetModemJSPR()
	if len(m.violations) > 0 {
		t.Fatalf("power sequence broken: %v", m.violations)
	}
	if len(m.edges) != 0 {
		t.Fatalf("I_EN edges %v on a booting modem, want none", m.edges)
	}
	if m.btd != 1 {
		t.Fatal("did not wait for the boot to finish")
	}
}

// A modem that is off (I_EN low, I_BTD low) is switched on.
func TestResetModemJSPR_ModemOffIsSwitchedOn(t *testing.T) {
	m := &fakeModem9704{ien: 0, btd: 0, bootReads: 5}
	withFakeModem(t, m).resetModemJSPR()
	if len(m.violations) > 0 || m.ien != 1 || m.btd != 1 {
		t.Fatalf("ien=%d btd=%d violations=%v, want a booted modem", m.ien, m.btd, m.violations)
	}
}

// If I_BTD never drops, the modem is still switched back on after the full
// wait, never left off, and never after a short fixed delay.
func TestResetModemJSPR_StuckBTDStillEndsWithTheModemOn(t *testing.T) {
	m := &fakeModem9704{ien: 1, btd: 1, stuckHigh: true}
	tr := withFakeModem(t, m)
	start := time.Now()
	tr.resetModemJSPR()
	if waited := time.Since(start); waited < imtShutdownWait {
		t.Fatalf("switched back on after %s, before the %s shutdown wait", waited, imtShutdownWait)
	}
	if m.ien != 1 {
		t.Fatal("modem left switched off")
	}
}
