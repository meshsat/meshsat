package main

import (
	"meshsat/internal/engine"
	"meshsat/internal/gateway"
)

// tleAdapter bridges engine.TLEManager to gateway.PassPredictor,
// breaking the engine→gateway→engine import cycle.
type tleAdapter struct {
	mgr *engine.TLEManager
}

func (a *tleAdapter) GeneratePasses(lat, lon, altKm float64, hours int, minElevDeg float64, startTime int64) ([]gateway.PassPrediction, error) {
	passes, err := a.mgr.GeneratePasses(lat, lon, altKm, hours, minElevDeg, startTime)
	if err != nil {
		return nil, err
	}

	out := make([]gateway.PassPrediction, len(passes))
	for i, p := range passes {
		out[i] = gateway.PassPrediction{
			Satellite:   p.Satellite,
			AOS:         p.AOS,
			LOS:         p.LOS,
			DurationMin: p.DurationMin,
			PeakElevDeg: p.PeakElevDeg,
			PeakAzimuth: p.PeakAzimuth,
			IsActive:    p.IsActive,
		}
	}
	return out, nil
}
