package api

import (
	"github.com/prometheus/client_golang/prometheus"

	"meshsat/internal/oob"
)

// oobCollector exposes the OOB management frame counters on every scrape,
// the same bridge pattern as the HeMB counters: no registration of
// individual series, the process-wide snapshot is read when asked.
// [MESHSAT-756]
type oobCollector struct {
	frames   *prometheus.Desc
	commands *prometheus.Desc
}

func newOOBCollector() *oobCollector {
	ns := "meshsat"
	return &oobCollector{
		frames:   prometheus.NewDesc(ns+"_oob_frames_total", "OOB management frames received by outcome (accepted, replay, bad_tag, unknown_peer, disabled_peer, disabled, no_key).", []string{"result"}, nil),
		commands: prometheus.NewDesc(ns+"_oob_commands_total", "OOB management commands executed by command and result code.", []string{"cmd", "result"}, nil),
	}
}

// Describe sends the descriptors.
func (c *oobCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.frames
	ch <- c.commands
}

// Collect reads the current counters.
func (c *oobCollector) Collect(ch chan<- prometheus.Metric) {
	frames, commands := oob.Global.Snapshot()
	for result, n := range frames {
		ch <- prometheus.MustNewConstMetric(c.frames, prometheus.CounterValue, float64(n), result)
	}
	for cmd, results := range commands {
		for result, n := range results {
			ch <- prometheus.MustNewConstMetric(c.commands, prometheus.CounterValue, float64(n), cmd, result)
		}
	}
}
