package hubreporter

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/rs/zerolog/log"
)

// CommandHandler processes commands received from the Hub via MQTT.
type CommandHandler struct {
	reporter *HubReporter
	bridgeID string
	handlers map[string]func(cmd Command) (json.RawMessage, error)
}

// NewCommandHandler creates a new CommandHandler that delegates to registered handlers.
// The healthFn callback is used by the built-in "ping" handler to return bridge health data.
func NewCommandHandler(reporter *HubReporter, bridgeID string, healthFn func() BridgeHealth) *CommandHandler {
	ch := &CommandHandler{
		reporter: reporter,
		bridgeID: bridgeID,
		handlers: make(map[string]func(cmd Command) (json.RawMessage, error)),
	}

	// Register built-in command handlers.
	ch.handlers["ping"] = func(cmd Command) (json.RawMessage, error) {
		health := healthFn()
		data, err := json.Marshal(health)
		if err != nil {
			return nil, fmt.Errorf("marshal health: %w", err)
		}
		return data, nil
	}

	ch.handlers["flush_burst"] = func(cmd Command) (json.RawMessage, error) {
		// Placeholder: burst queue flush requires gateway access.
		log.Info().Str("request_id", cmd.RequestID).Msg("commander: flush_burst requested (not wired)")
		return json.RawMessage(`{"message":"flush_burst not available"}`), nil
	}

	ch.handlers["send_text"] = func(cmd Command) (json.RawMessage, error) {
		// Placeholder: actual text sending requires gateway access.
		log.Info().Str("request_id", cmd.RequestID).Str("target", cmd.TargetDevice).Msg("commander: send_text requested (placeholder)")
		return json.RawMessage(`{"message":"send_text accepted (placeholder)"}`), nil
	}

	ch.handlers["send_mt"] = func(cmd Command) (json.RawMessage, error) {
		// Placeholder: actual MT sending requires satellite transport access.
		log.Info().Str("request_id", cmd.RequestID).Str("target", cmd.TargetDevice).Msg("commander: send_mt requested (placeholder)")
		return json.RawMessage(`{"message":"send_mt accepted (placeholder)"}`), nil
	}

	ch.handlers["config_update"] = func(cmd Command) (json.RawMessage, error) {
		// Placeholder: config updates require config manager access.
		log.Info().Str("request_id", cmd.RequestID).Msg("commander: config_update requested (placeholder)")
		return json.RawMessage(`{"message":"config_update accepted (placeholder)"}`), nil
	}

	ch.handlers["reboot"] = func(cmd Command) (json.RawMessage, error) {
		// NEVER actually reboot without explicit confirmation.
		// Actual execution deferred to explicit approval mechanism.
		log.Warn().Str("request_id", cmd.RequestID).Msg("commander: reboot requested — NOT executing (requires explicit approval)")
		return json.RawMessage(`{"message":"reboot acknowledged but not executed (requires explicit approval)"}`), nil
	}

	return ch
}

// HandleCommand processes a command payload received from the Hub.
// It parses the command, looks up the handler, executes it, and publishes the response.
func (ch *CommandHandler) HandleCommand(payload []byte) {
	var cmd Command
	if err := json.Unmarshal(payload, &cmd); err != nil {
		log.Warn().Err(err).Msg("commander: invalid command JSON")
		return
	}

	// Validate protocol version.
	if cmd.Protocol != ProtocolVersion {
		log.Warn().
			Str("protocol", cmd.Protocol).
			Str("expected", ProtocolVersion).
			Msg("commander: unknown protocol version, ignoring command")
		ch.sendErrorResponse(cmd, "unsupported protocol version: "+cmd.Protocol)
		return
	}

	if cmd.Cmd == "" {
		log.Warn().Str("request_id", cmd.RequestID).Msg("commander: empty cmd field")
		ch.sendErrorResponse(cmd, "cmd field is required")
		return
	}

	handler, ok := ch.handlers[cmd.Cmd]
	if !ok {
		log.Warn().Str("cmd", cmd.Cmd).Str("request_id", cmd.RequestID).Msg("commander: unknown command")
		ch.sendErrorResponse(cmd, fmt.Sprintf("unknown command: %s", cmd.Cmd))
		return
	}

	log.Info().
		Str("cmd", cmd.Cmd).
		Str("request_id", cmd.RequestID).
		Str("target", cmd.TargetDevice).
		Msg("commander: executing command")

	result, err := handler(cmd)
	if err != nil {
		log.Error().Err(err).Str("cmd", cmd.Cmd).Str("request_id", cmd.RequestID).Msg("commander: command execution failed")
		ch.sendErrorResponse(cmd, err.Error())
		return
	}

	ch.sendOkResponse(cmd, result)
}

// sendOkResponse publishes a successful command response to the Hub.
func (ch *CommandHandler) sendOkResponse(cmd Command, result json.RawMessage) {
	resp := CommandResponse{
		Protocol:  ProtocolVersion,
		RequestID: cmd.RequestID,
		Cmd:       cmd.Cmd,
		Status:    "ok",
		Result:    result,
		Timestamp: time.Now().UTC(),
	}
	ch.publishResponse(resp)
}

// sendErrorResponse publishes an error command response to the Hub.
func (ch *CommandHandler) sendErrorResponse(cmd Command, errMsg string) {
	resp := CommandResponse{
		Protocol:  ProtocolVersion,
		RequestID: cmd.RequestID,
		Cmd:       cmd.Cmd,
		Status:    "error",
		Error:     errMsg,
		Timestamp: time.Now().UTC(),
	}
	ch.publishResponse(resp)
}

// publishResponse marshals and publishes a CommandResponse via the HubReporter.
func (ch *CommandHandler) publishResponse(resp CommandResponse) {
	if ch.reporter == nil || !ch.reporter.IsConnected() {
		log.Warn().Str("request_id", resp.RequestID).Msg("commander: cannot publish response (not connected)")
		return
	}

	topic := TopicBridgeCmdResp(ch.bridgeID)
	if err := ch.reporter.publish(topic, 1, false, resp); err != nil {
		log.Error().Err(err).Str("request_id", resp.RequestID).Msg("commander: failed to publish response")
	} else {
		log.Debug().
			Str("request_id", resp.RequestID).
			Str("cmd", resp.Cmd).
			Str("status", resp.Status).
			Msg("commander: response published")
	}
}
