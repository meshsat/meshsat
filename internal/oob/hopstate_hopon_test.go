package oob

import "context"

// fixedHopSet is a switch for tests of the exchange alone: always interval
// 0, on as f says, and a hold that nothing waits on.
type fixedHopSet struct{ on func() bool }

func (fixedHopSet) OnOff(func())                     {}
func (fixedHopSet) SetOnChange(func(uint64))         {}
func (fixedHopSet) Go(uint64, func(context.Context)) {}
func (f fixedHopSet) Hold() (func(), uint64, bool)   { return func() {}, 0, f.on() }

// setHopOn wires the media hopping switch into a harness, in interval 0.
func setHopOn(h *harness, f func() bool) {
	h.svc.d.HopSession = func() (uint64, bool) { return 0, f() }
	h.svc.hopSet = fixedHopSet{on: f}
}
