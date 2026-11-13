package main

import (
	"context"
	"time"

	"github.com/rs/zerolog/log"

	"meshsat/internal/database"
	"meshsat/internal/engine"
)

// deadManSOS is what the dead man's switch needs from the API server: a way
// to raise the SOS with the last known position as a fallback, and to hand
// the switch to the API that arms it (GET/POST /api/deadman).
type deadManSOS interface {
	TriggerSOSWithFallback(trigger string, lat, lon float64) bool
	SetDeadManSwitch(*engine.DeadManSwitch)
}

// deadManTAK is the TAK gateway's alarm, when the kit has one.
type deadManTAK interface {
	SendOwnDeadman(lat, lon float64, lastSeen time.Time) error
}

// wireDeadMan builds, wires and starts the dead man's switch of the shipped
// binary: when it fires it raises the SOS (with its last known position as
// a fallback) and the TAK alarm. main.go calls it; deadman_wiring_test.go
// holds that it does, so the switch can never again exist only in app.go or
// in the tests. Off until enabled through the API. [MESHSAT-996, MESHSAT-1136]
func wireDeadMan(ctx context.Context, db *database.DB, sos deadManSOS, tak func() deadManTAK, timeout time.Duration) *engine.DeadManSwitch {
	deadman := engine.NewDeadManSwitch(db, timeout)
	deadman.SetSOSCallback(func(lat, lon float64, lastSeen time.Time) {
		log.Warn().
			Time("last_activity", lastSeen).
			Float64("lat", lat).Float64("lon", lon).
			Msg("dead man's switch fired, raising SOS")
		// The alarm on TAK, as MeshSat Android's sendDeadman, in the
		// background so it never holds the SOS up. [MESHSAT-1421]
		if tak != nil {
			if tg := tak(); tg != nil {
				go func() {
					if err := tg.SendOwnDeadman(lat, lon, lastSeen); err != nil {
						log.Warn().Err(err).Msg("dead man's switch: the CoT alarm did not reach every TAK output")
					}
				}()
			}
		}
		sos.TriggerSOSWithFallback("deadman", lat, lon)
	})
	deadman.Start(ctx)
	sos.SetDeadManSwitch(deadman)
	return deadman
}
