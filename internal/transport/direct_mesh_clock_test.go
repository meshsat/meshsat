package transport

import "testing"

// The radio's RTC is only written when the host clock has been established.
// A kit that booted with no time source would otherwise set its own radio 8 h
// wrong on every handshake, and the radio then stamps every packet with it.
// No check installed must behave exactly as before. [MESHSAT-1056]
func TestClockTrustedForRadio(t *testing.T) {
	tests := []struct {
		name string
		set  func(*DirectMeshTransport)
		want bool
	}{
		{"no check installed", func(*DirectMeshTransport) {}, true},
		{"clock trusted", func(tr *DirectMeshTransport) { tr.SetClockTrustFn(func() bool { return true }) }, true},
		{"clock not trusted", func(tr *DirectMeshTransport) { tr.SetClockTrustFn(func() bool { return false }) }, false},
		{"nil function clears the check", func(tr *DirectMeshTransport) {
			tr.SetClockTrustFn(func() bool { return false })
			tr.SetClockTrustFn(nil)
		}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tr := NewDirectMeshTransport("auto")
			tt.set(tr)
			if got := tr.clockTrustedForRadio(); got != tt.want {
				t.Errorf("clockTrustedForRadio() = %v, want %v", got, tt.want)
			}
		})
	}
}
