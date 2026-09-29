package api

import (
	"errors"

	"github.com/rs/zerolog/log"

	"meshsat/internal/gateway"
)

// The Bridge's own TAK events that start in the API, as MeshSat Android sends
// them (TakIntegration): a CoT emergency when an SOS starts, and a GeoChat for
// every text the app sends to the mesh. They go out in the background through
// the running TAK gateway's outputs (server, SA multicast, Hub); without a TAK
// gateway nothing happens. [MESHSAT-1421]

// takGateway is the running TAK gateway, or nil.
func (s *Server) takGateway() *gateway.TAKGateway {
	if s.gwManager == nil {
		return nil
	}
	return s.gwManager.GetTAKGateway()
}

// takOwnSOS puts an SOS that just started on TAK, at this Bridge's position.
// The TAK gateway is looked up in the background with the send: the lookup
// takes the gateway manager's lock, which a gateway being stopped can hold
// for a whole modem session, and the start of an SOS must not wait for it.
// [MESHSAT-1430]
func (s *Server) takOwnSOS(text string) {
	go func() {
		tg := s.takGateway()
		if tg == nil {
			return
		}
		err := tg.SendOwnSOS(text)
		switch {
		case errors.Is(err, gateway.ErrTAKNoPosition):
			log.Info().Msg("SOS: this Bridge knows no position of its own, no CoT emergency for TAK")
		case err != nil:
			log.Warn().Err(err).Msg("SOS: the CoT emergency did not reach every TAK output")
		}
	}()
}

// takOwnChat puts a text the app sent to the mesh on TAK as GeoChat.
func (s *Server) takOwnChat(text string) {
	tg := s.takGateway()
	if tg == nil {
		return
	}
	go func() {
		if err := tg.SendOwnChat(text); err != nil {
			log.Warn().Err(err).Msg("tak: the GeoChat of a sent text did not reach every TAK output")
		}
	}()
}
