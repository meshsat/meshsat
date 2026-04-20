package hubreporter

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/rs/zerolog/log"
)

func decodeBase64(s string) ([]byte, error) {
	return base64.StdEncoding.DecodeString(s)
}

// CommandDeps holds optional subsystem references for command execution.
type CommandDeps struct {
	// SendText queues a text message to an interface via the delivery pipeline.
	// Parameters: interfaceID, text. Returns deliveryID, msgRef, error.
	SendText func(interfaceID, text string) (int64, string, error)

	// SendMT sends a message via satellite (Iridium MO). Returns error.
	SendMT func(ctx context.Context, payload []byte) error

	// FlushBurst flushes the burst queue and returns the payload.
	FlushBurst func(ctx context.Context) ([]byte, int, error)

	// BurstPending returns the number of pending burst messages.
	BurstPending func() int
}

// CredentialStore is the interface for storing credentials received from Hub.
type CredentialStore interface {
	StoreCredential(id, provider, name, credType string, encryptedData []byte, certNotAfter, certFingerprint string, version int) error
	RemoveCredential(id string) error
}

// CommandHandler processes commands received from the Hub via MQTT.
type CommandHandler struct {
	reporter   *HubReporter
	bridgeID   string
	handlers   map[string]func(cmd Command) (json.RawMessage, error)
	deps       CommandDeps
	credStore  CredentialStore
	keyStore   KeyStoreImporter // [MESHSAT-447]
	dirApplier DirectoryApplier // [MESHSAT-540]
	trustStore TrustAnchorStore // [MESHSAT-539/540]
}

// NewCommandHandler creates a new CommandHandler that delegates to registered handlers.
// The healthFn callback is used by the built-in "ping" handler to return bridge health data.
func NewCommandHandler(reporter *HubReporter, bridgeID string, healthFn func() BridgeHealth) *CommandHandler {
	ch := &CommandHandler{
		reporter: reporter,
		bridgeID: bridgeID,
		handlers: make(map[string]func(cmd Command) (json.RawMessage, error)),
	}

	ch.handlers["ping"] = func(cmd Command) (json.RawMessage, error) {
		health := healthFn()
		data, err := json.Marshal(health)
		if err != nil {
			return nil, fmt.Errorf("marshal health: %w", err)
		}
		return data, nil
	}

	ch.handlers["flush_burst"] = ch.handleFlushBurst
	ch.handlers["send_text"] = ch.handleSendText
	ch.handlers["send_mt"] = ch.handleSendMT

	ch.handlers["config_update"] = func(cmd Command) (json.RawMessage, error) {
		// Config updates require careful validation — accept but log only.
		log.Info().Str("request_id", cmd.RequestID).Msg("commander: config_update received (not implemented)")
		return json.RawMessage(`{"message":"config_update not yet implemented"}`), nil
	}

	ch.handlers["reboot"] = func(cmd Command) (json.RawMessage, error) {
		log.Warn().Str("request_id", cmd.RequestID).Msg("commander: reboot requested — NOT executing (requires explicit approval)")
		return json.RawMessage(`{"message":"reboot acknowledged but not executed (requires explicit approval)"}`), nil
	}

	ch.handlers["credential_push"] = ch.handleCredentialPush
	ch.handlers["credential_revoke"] = ch.handleCredentialRevoke
	ch.handlers["key_rotate"] = ch.handleKeyRotate

	// Directory sync from Hub [MESHSAT-540]. Handlers fail-closed if
	// their dependencies (DirectoryApplier, TrustAnchorStore) have
	// not been wired — the bridge still boots, and the operator sees
	// an explicit rejection message on the first push, rather than a
	// silent no-op.
	ch.handlers["directory_push"] = ch.handleDirectoryPush
	ch.handlers["directory_trust_anchor_rotate"] = ch.handleDirectoryTrustAnchorRotate

	// Host-level BT/WiFi management via Hub MQTT. [MESHSAT-632]
	ch.registerSysMgmtHandlers()

	return ch
}

// KeyStoreImporter is the interface for importing keys from Hub commands. [MESHSAT-447]
type KeyStoreImporter interface {
	StoreKey(channelType, address string, rawKey []byte) (int, error)
}

// SetKeyStore sets the key store for key_rotate commands. [MESHSAT-447]
func (ch *CommandHandler) SetKeyStore(ks KeyStoreImporter) {
	ch.keyStore = ks
}

// SetCredentialStore sets the credential storage for credential push/revoke handlers.
func (ch *CommandHandler) SetCredentialStore(cs CredentialStore) {
	ch.credStore = cs
}

// SetDeps sets the subsystem dependencies for command execution.
func (ch *CommandHandler) SetDeps(deps CommandDeps) {
	ch.deps = deps
}

// handleFlushBurst flushes the satellite burst queue.
func (ch *CommandHandler) handleFlushBurst(cmd Command) (json.RawMessage, error) {
	if ch.deps.FlushBurst == nil {
		return nil, fmt.Errorf("burst queue not available")
	}
	payload, count, err := ch.deps.FlushBurst(context.Background())
	if err != nil {
		return nil, fmt.Errorf("flush burst: %w", err)
	}
	result := struct {
		Messages int `json:"messages_flushed"`
		Bytes    int `json:"payload_bytes"`
	}{count, len(payload)}
	data, _ := json.Marshal(result)
	log.Info().Int("messages", count).Int("bytes", len(payload)).
		Str("request_id", cmd.RequestID).Msg("commander: burst queue flushed")
	return data, nil
}

// sendTextPayload is the expected JSON payload for send_text commands.
type sendTextPayload struct {
	InterfaceID string `json:"interface_id"` // target interface (e.g. "iridium_imt_0", "mesh_0")
	Text        string `json:"text"`
}

// handleSendText sends a text message via the delivery pipeline.
func (ch *CommandHandler) handleSendText(cmd Command) (json.RawMessage, error) {
	if ch.deps.SendText == nil {
		return nil, fmt.Errorf("dispatcher not available")
	}

	var p sendTextPayload
	if err := json.Unmarshal(cmd.Payload, &p); err != nil {
		return nil, fmt.Errorf("invalid send_text payload: %w", err)
	}
	if p.Text == "" {
		return nil, fmt.Errorf("text is required")
	}
	if p.InterfaceID == "" {
		return nil, fmt.Errorf("interface_id is required")
	}

	deliveryID, msgRef, err := ch.deps.SendText(p.InterfaceID, p.Text)
	if err != nil {
		return nil, fmt.Errorf("queue send: %w", err)
	}

	result := struct {
		DeliveryID int64  `json:"delivery_id"`
		MsgRef     string `json:"msg_ref"`
	}{deliveryID, msgRef}
	data, _ := json.Marshal(result)
	log.Info().Int64("delivery_id", deliveryID).Str("msg_ref", msgRef).
		Str("interface", p.InterfaceID).Str("request_id", cmd.RequestID).
		Msg("commander: text queued for delivery")
	return data, nil
}

// sendMTPayload is the expected JSON payload for send_mt commands.
type sendMTPayload struct {
	Data string `json:"data"` // base64-encoded payload
	Text string `json:"text"` // plain text (alternative to data)
}

// handleSendMT sends a message via satellite (queued through delivery pipeline).
func (ch *CommandHandler) handleSendMT(cmd Command) (json.RawMessage, error) {
	if ch.deps.SendText == nil {
		return nil, fmt.Errorf("dispatcher not available")
	}

	var p sendMTPayload
	if err := json.Unmarshal(cmd.Payload, &p); err != nil {
		return nil, fmt.Errorf("invalid send_mt payload: %w", err)
	}

	text := p.Text
	if text == "" && p.Data != "" {
		// Decode base64 payload
		decoded, err := decodeBase64(p.Data)
		if err != nil {
			return nil, fmt.Errorf("invalid base64 data: %w", err)
		}
		text = string(decoded)
	}
	if text == "" {
		return nil, fmt.Errorf("text or data is required")
	}

	// Route to first available satellite interface
	ifaceID := "iridium_imt_0"
	deliveryID, msgRef, err := ch.deps.SendText(ifaceID, text)
	if err != nil {
		return nil, fmt.Errorf("queue MT: %w", err)
	}

	result := struct {
		DeliveryID int64  `json:"delivery_id"`
		MsgRef     string `json:"msg_ref"`
		Interface  string `json:"interface"`
	}{deliveryID, msgRef, ifaceID}
	data, _ := json.Marshal(result)
	log.Info().Int64("delivery_id", deliveryID).Str("msg_ref", msgRef).
		Str("request_id", cmd.RequestID).Msg("commander: MT queued for satellite delivery")
	return data, nil
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

// handleCredentialPush stores a credential received from the Hub.
func (ch *CommandHandler) handleCredentialPush(cmd Command) (json.RawMessage, error) {
	if ch.credStore == nil {
		return nil, fmt.Errorf("credential store not configured")
	}

	var payload struct {
		CredentialID    string `json:"credential_id"`
		Provider        string `json:"provider"`
		Name            string `json:"name"`
		CredType        string `json:"cred_type"`
		Version         int    `json:"version"`
		Data            []byte `json:"data"` // base64-decoded by json.Unmarshal
		CertNotAfter    string `json:"cert_not_after"`
		CertFingerprint string `json:"cert_fingerprint"`
	}
	if err := json.Unmarshal(cmd.Payload, &payload); err != nil {
		return nil, fmt.Errorf("parse credential_push payload: %w", err)
	}

	if err := ch.credStore.StoreCredential(
		payload.CredentialID, payload.Provider, payload.Name, payload.CredType,
		payload.Data, payload.CertNotAfter, payload.CertFingerprint, payload.Version,
	); err != nil {
		return nil, fmt.Errorf("store credential: %w", err)
	}

	log.Info().Str("id", payload.CredentialID).Str("provider", payload.Provider).
		Str("name", payload.Name).Int("version", payload.Version).
		Msg("commander: credential received from Hub")

	return json.RawMessage(`{"applied":true}`), nil
}

// handleCredentialRevoke removes a credential from the bridge.
func (ch *CommandHandler) handleCredentialRevoke(cmd Command) (json.RawMessage, error) {
	if ch.credStore == nil {
		return nil, fmt.Errorf("credential store not configured")
	}

	var payload struct {
		CredentialID string `json:"credential_id"`
		Reason       string `json:"reason"`
	}
	if err := json.Unmarshal(cmd.Payload, &payload); err != nil {
		return nil, fmt.Errorf("parse credential_revoke payload: %w", err)
	}

	if err := ch.credStore.RemoveCredential(payload.CredentialID); err != nil {
		log.Warn().Err(err).Str("id", payload.CredentialID).Msg("commander: credential revoke failed (may not exist)")
	}

	log.Info().Str("id", payload.CredentialID).Str("reason", payload.Reason).
		Msg("commander: credential revoked by Hub")

	return json.RawMessage(`{"revoked":true}`), nil
}

// handleKeyRotate receives a new channel encryption key from the Hub.
// Stores in keystore (old key retired with grace period). [MESHSAT-447]
func (ch *CommandHandler) handleKeyRotate(cmd Command) (json.RawMessage, error) {
	if ch.keyStore == nil {
		return nil, fmt.Errorf("keystore not configured")
	}

	var payload struct {
		ChannelType string `json:"channel_type"` // e.g. "sms"
		Address     string `json:"address"`      // e.g. "+31653618463"
		KeyHex      string `json:"key_hex"`      // hex-encoded AES-256 key
		Version     int    `json:"version"`      // Hub's version number
	}
	if err := json.Unmarshal(cmd.Payload, &payload); err != nil {
		return nil, fmt.Errorf("parse key_rotate payload: %w", err)
	}

	if payload.ChannelType == "" || payload.Address == "" || payload.KeyHex == "" {
		return nil, fmt.Errorf("key_rotate requires channel_type, address, and key_hex")
	}

	rawKey, err := hex.DecodeString(payload.KeyHex)
	if err != nil {
		return nil, fmt.Errorf("invalid key_hex: %w", err)
	}

	localVersion, err := ch.keyStore.StoreKey(payload.ChannelType, payload.Address, rawKey)
	if err != nil {
		return nil, fmt.Errorf("store key: %w", err)
	}

	log.Info().
		Str("channel", payload.ChannelType).
		Str("address", payload.Address).
		Int("hub_version", payload.Version).
		Int("local_version", localVersion).
		Msg("commander: channel key rotated from Hub")

	result, _ := json.Marshal(map[string]interface{}{
		"applied":       true,
		"local_version": localVersion,
	})
	return result, nil
}
