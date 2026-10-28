package hubreporter

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
)

func decodeBase64(s string) ([]byte, error) {
	return base64.StdEncoding.DecodeString(s)
}

// CommandDeps holds optional subsystem references for command execution.
type CommandDeps struct {
	// SendTextKeyed is SendText under the command's request id and argument
	// digest: run again for the same command (after a restart whose reply
	// was never stored), it answers the delivery it queued the first time
	// (FA-R1). Used when set and the command has a request id. [MESHSAT-1584]
	SendTextKeyed func(requestID, fingerprint, interfaceID, text string) (int64, string, error)
	// SendText queues a text message to an interface via the delivery pipeline.
	// Parameters: interfaceID, text. Returns deliveryID, msgRef, error.
	SendText func(interfaceID, text string) (int64, string, error)
	// SendTextTo queues a text to ONE node of a mesh interface, send_text's
	// target_device, as the delivery's destination. [MESHSAT-1705]
	SendTextTo func(interfaceID, destination, text string) (int64, string, error)
	// SendTextToKeyed is SendTextTo under the command's request id and
	// argument digest, as SendTextKeyed.
	SendTextToKeyed func(requestID, fingerprint, interfaceID, destination, text string) (int64, string, error)

	// SendMT sends a message via satellite (Iridium MO). Returns error.
	SendMT func(ctx context.Context, payload []byte) error
	// SatInterface names the kit's satellite link as it is today, whichever
	// modem it carries (iridium_0 for a 9602/9603, iridium_imt_0 for a 9704),
	// for a send_mt that names none; nil keeps the old iridium_imt_0.
	// [MESHSAT-1745]
	SatInterface func() (string, bool)

	// FlushBurst flushes the burst queue and returns the payload.
	FlushBurst func(ctx context.Context) ([]byte, int, error)

	// BurstPending returns the number of pending burst messages.
	BurstPending func() int
	// Mgmt executes an OOB management command locally (Hub-originated),
	// sharing the executor and audit path of frames received over a
	// bearer. Nil = the OOB service is not wired. [MESHSAT-756]
	Mgmt func(ctx context.Context, req MgmtRequest) (MgmtResult, error)
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
	// mgmtPeer registers the OOB peer for a Hub-provisioned management
	// key (key_rotate with channel_type "mgmt"). [MESHSAT-964]
	mgmtPeer func(alias string, rawKey []byte) (uint16, error)
	// cmdLog remembers request ids, so a command delivered twice runs
	// once; runID names this process in it. [MESHSAT-1584]
	cmdLog CommandLog
	runID  string
	// onResponse sees every reply before it is published (tests).
	onResponse func(CommandResponse)
}

// CommandClaim is what the command log found for a request id: run it now
// (Fresh: new, or claimed by a process that died before it finished), its
// first reply (Response, empty while it runs here), or the id seen with
// other arguments (Conflict).
type CommandClaim struct {
	Fresh    bool
	Response string
	Conflict bool
}

// CommandLog keeps each Hub command's request id, its arguments' digest,
// the process running it and its reply for a day. The request ids are the
// Hub's, on this Bridge's own command topic, so they need no scope beyond
// it. [MESHSAT-1584]
type CommandLog interface {
	ClaimHubCommand(requestID, cmd, argsSHA256, runID string, now time.Time) (CommandClaim, error)
	// FinishHubCommand stores a reply only under the claim of runID with
	// these arguments, and only while none is stored.
	FinishHubCommand(requestID, runID, argsSHA256, response string) error
}

// CommandArgsDigest is the digest of what a command asks for: its name,
// target and payload bytes.
func CommandArgsDigest(cmd Command) string { return commandArgsDigest(cmd) }

func commandArgsDigest(cmd Command) string {
	h := sha256.New()
	h.Write([]byte(cmd.Cmd + "\x00" + cmd.TargetDevice + "\x00"))
	h.Write(cmd.Payload)
	return hex.EncodeToString(h.Sum(nil))
}

// SetCommandLog makes a repeated request id run once: the broker delivers
// a QoS 1 command again after a reconnect, and a Hub send_text then queued
// the text twice.
func (ch *CommandHandler) SetCommandLog(l CommandLog) { ch.cmdLog = l }

// SetResponseObserver sees every reply before it is published.
func (ch *CommandHandler) SetResponseObserver(fn func(CommandResponse)) { ch.onResponse = fn }

// NewCommandHandler creates a new CommandHandler that delegates to registered handlers.
// The healthFn callback is used by the built-in "ping" handler to return bridge health data.
func NewCommandHandler(reporter *HubReporter, bridgeID string, healthFn func() BridgeHealth) *CommandHandler {
	run := make([]byte, 8)
	_, _ = rand.Read(run)
	ch := &CommandHandler{
		reporter: reporter,
		bridgeID: bridgeID,
		handlers: make(map[string]func(cmd Command) (json.RawMessage, error)),
		runID:    hex.EncodeToString(run),
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

	// Host reboot through the OOB executor: executes only with an explicit
	// confirm flag in the payload, refused otherwise. [MESHSAT-756]
	ch.handlers["reboot"] = func(cmd Command) (json.RawMessage, error) {
		req, err := ch.mgmtRequest(cmd, "REBOOT")
		if err != nil {
			return nil, err
		}
		if !req.Confirm {
			log.Warn().Str("request_id", cmd.RequestID).Msg("commander: reboot requested without confirm, not executing")
			return json.RawMessage(`{"message":"reboot acknowledged but not executed (payload needs \"confirm\": true)"}`), nil
		}
		return ch.execMgmt(cmd, req)
	}

	// OOB management commands by name (PING, STATUS-NET, LOG, RESET,
	// BEARER, RESTART). [MESHSAT-756]
	for name, fixed := range map[string]string{
		"mgmt_ping":    "PING",
		"mgmt_status":  "STATUS-NET",
		"mgmt_log":     "LOG",
		"mgmt_reset":   "RESET",
		"mgmt_bearer":  "BEARER",
		"mgmt_restart": "RESTART",
	} {
		fixedCmd := fixed
		ch.handlers[name] = func(cmd Command) (json.RawMessage, error) {
			req, err := ch.mgmtRequest(cmd, fixedCmd)
			if err != nil {
				return nil, err
			}
			return ch.execMgmt(cmd, req)
		}
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
	ch.handlers["directory_rotate"] = ch.handleDirectoryRotate

	// Host-level BT/WiFi management via Hub MQTT. [MESHSAT-632]
	ch.registerSysMgmtHandlers()

	return ch
}

// mgmtRequest decodes a management payload and pins the command name.
func (ch *CommandHandler) mgmtRequest(cmd Command, fixedCmd string) (MgmtRequest, error) {
	var req MgmtRequest
	if len(cmd.Payload) > 0 {
		if err := json.Unmarshal(cmd.Payload, &req); err != nil {
			return req, fmt.Errorf("invalid payload: %w", err)
		}
	}
	req.Cmd = fixedCmd
	return req, nil
}

// execMgmt runs a management command through the wired executor.
func (ch *CommandHandler) execMgmt(cmd Command, req MgmtRequest) (json.RawMessage, error) {
	if ch.deps.Mgmt == nil {
		return nil, fmt.Errorf("management commands not available on this bridge")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	res, err := ch.deps.Mgmt(ctx, req)
	if err != nil {
		return nil, err
	}
	log.Info().Str("request_id", cmd.RequestID).Str("cmd", req.Cmd).Str("result", res.Result).Msg("commander: management command executed")
	return json.Marshal(res)
}

// KeyStoreImporter is the interface for importing keys from Hub commands. [MESHSAT-447]
type KeyStoreImporter interface {
	StoreKey(channelType, address string, rawKey []byte) (int, error)
}

// labelledKeyStore is a KeyStoreImporter that also keeps where a key came
// from (the keystore does).
type labelledKeyStore interface {
	StoreKeyLabelled(channelType, address string, rawKey []byte, label string) (int, error)
}

// SetKeyStore sets the key store for key_rotate commands. [MESHSAT-447]
func (ch *CommandHandler) SetKeyStore(ks KeyStoreImporter) {
	ch.keyStore = ks
}

// SetMgmtPeerRegistrar wires the OOB service so a key_rotate carrying
// channel_type "mgmt" also registers the peer that key belongs to. Without
// it the key is stored and unusable: an inbound frame is rejected as
// unknown_peer and no reply can be sealed. [MESHSAT-964]
func (ch *CommandHandler) SetMgmtPeerRegistrar(fn func(alias string, rawKey []byte) (uint16, error)) {
	ch.mgmtPeer = fn
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

// sendText queues a command's text: keyed by the command when it can be.
func (ch *CommandHandler) sendText(cmd Command, ifaceID, text string) (int64, string, error) {
	if ch.deps.SendTextKeyed != nil && cmd.RequestID != "" {
		return ch.deps.SendTextKeyed(cmd.RequestID, commandArgsDigest(cmd), ifaceID, text)
	}
	return ch.deps.SendText(ifaceID, text)
}

// sendTextTo queues a command's text to one node: keyed by the command when it
// can be.
func (ch *CommandHandler) sendTextTo(cmd Command, ifaceID, dest, text string) (int64, string, error) {
	if ch.deps.SendTextToKeyed != nil && cmd.RequestID != "" {
		return ch.deps.SendTextToKeyed(cmd.RequestID, commandArgsDigest(cmd), ifaceID, dest, text)
	}
	return ch.deps.SendTextTo(ifaceID, dest, text)
}

// meshNodeID reports whether s names one mesh node, "!" and eight hex digits,
// and returns it in lower case.
func meshNodeID(s string) (string, bool) {
	hexID, ok := strings.CutPrefix(strings.TrimSpace(s), "!")
	if !ok || len(hexID) != 8 {
		return "", false
	}
	if _, err := strconv.ParseUint(hexID, 16, 32); err != nil {
		return "", false
	}
	return "!" + strings.ToLower(hexID), true
}

// handleSendText sends a text message via the delivery pipeline. With a
// target_device ("!aabbccdd") it goes to that one node of a mesh interface
// (mesh_0 when interface_id is empty); without one, to the interface as
// before. Until MESHSAT-1705 target_device was read nowhere and a text the Hub
// meant for one node went to the whole mesh.
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

	var (
		dest       string
		deliveryID int64
		msgRef     string
		err        error
	)
	if strings.TrimSpace(cmd.TargetDevice) != "" {
		var ok bool
		if dest, ok = meshNodeID(cmd.TargetDevice); !ok {
			return nil, fmt.Errorf("target_device %q is not a mesh node id (!aabbccdd)", cmd.TargetDevice)
		}
		if p.InterfaceID == "" {
			p.InterfaceID = "mesh_0"
		}
		if !strings.HasPrefix(p.InterfaceID, "mesh_") {
			return nil, fmt.Errorf("target_device names a mesh node; interface_id %q is not a mesh interface", p.InterfaceID)
		}
		if ch.deps.SendTextTo == nil {
			return nil, fmt.Errorf("this bridge cannot address one mesh node")
		}
		deliveryID, msgRef, err = ch.sendTextTo(cmd, p.InterfaceID, dest, p.Text)
	} else {
		if p.InterfaceID == "" {
			return nil, fmt.Errorf("interface_id is required")
		}
		deliveryID, msgRef, err = ch.sendText(cmd, p.InterfaceID, p.Text)
	}
	if err != nil {
		return nil, fmt.Errorf("queue send: %w", err)
	}

	result := struct {
		DeliveryID  int64  `json:"delivery_id"`
		MsgRef      string `json:"msg_ref"`
		Destination string `json:"destination,omitempty"`
	}{deliveryID, msgRef, dest}
	data, _ := json.Marshal(result)
	log.Info().Int64("delivery_id", deliveryID).Str("msg_ref", msgRef).
		Str("interface", p.InterfaceID).Str("destination", dest).Str("request_id", cmd.RequestID).
		Msg("commander: text queued for delivery")
	return data, nil
}

// sendMTPayload is the expected JSON payload for send_mt commands.
type sendMTPayload struct {
	Data string `json:"data"` // base64-encoded payload
	Text string `json:"text"` // plain text (alternative to data)
	// Interface names the satellite interface to queue on; empty = the
	// kit's satellite link of the day (deps.SatInterface). [MESHSAT-1745]
	Interface string `json:"interface,omitempty"`
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

	// The satellite link as it is today: the payload's choice, else the
	// kit's active modem, else the 9704 name every kit once had.
	ifaceID := strings.TrimSpace(p.Interface)
	if ifaceID == "" && ch.deps.SatInterface != nil {
		if id, ok := ch.deps.SatInterface(); ok && id != "" {
			ifaceID = id
		}
	}
	if ifaceID == "" {
		ifaceID = "iridium_imt_0"
	}
	deliveryID, msgRef, err := ch.sendText(cmd, ifaceID, text)
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
		ch.refuse(cmd, "unsupported protocol version: "+cmd.Protocol)
		return
	}

	if cmd.Cmd == "" {
		log.Warn().Str("request_id", cmd.RequestID).Msg("commander: empty cmd field")
		ch.refuse(cmd, "cmd field is required")
		return
	}

	handler, ok := ch.handlers[cmd.Cmd]
	if !ok {
		log.Warn().Str("cmd", cmd.Cmd).Str("request_id", cmd.RequestID).Msg("commander: unknown command")
		ch.refuse(cmd, fmt.Sprintf("unknown command: %s", cmd.Cmd))
		return
	}

	// claimed: this invocation holds the command's log entry, and only then
	// may it store the reply. A command that ran without a claim (the log
	// was down) publishes its reply and stores nothing, or it could finish
	// another invocation's unfinished entry under the same id (review
	// 2026-10-18 19:34, FA-R3).
	claimed := false
	if ch.cmdLog != nil && cmd.RequestID != "" {
		c, err := ch.cmdLog.ClaimHubCommand(cmd.RequestID, cmd.Cmd, commandArgsDigest(cmd), ch.runID, time.Now())
		fresh, prev := c.Fresh, c.Response
		claimed = err == nil && fresh
		switch {
		case err != nil:
			// Without the log a repeat cannot be told apart: only a command
			// whose own admission is keyed (send_text, send_mt) or that
			// changes nothing (ping) still runs (FA-R1).
			if !ch.safeWithoutLog(cmd.Cmd) {
				log.Warn().Err(err).Str("cmd", cmd.Cmd).Str("request_id", cmd.RequestID).Msg("commander: command log unavailable; a command that changes something is not run without it")
				ch.refuse(cmd, "the Bridge cannot record this command right now; not run, send it again")
				return
			}
			log.Warn().Err(err).Str("cmd", cmd.Cmd).Str("request_id", cmd.RequestID).Msg("commander: command log unavailable; running a command that is safe without it")
		case c.Conflict:
			log.Warn().Str("cmd", cmd.Cmd).Str("request_id", cmd.RequestID).Msg("commander: a request id came again with other arguments; not run")
			ch.refuse(cmd, "request id "+cmd.RequestID+" was used before with other arguments; not run")
			return
		case !fresh && prev == "":
			log.Info().Str("cmd", cmd.Cmd).Str("request_id", cmd.RequestID).Msg("commander: the same command came again while it runs; not run twice")
			return
		case !fresh:
			var resp CommandResponse
			if err := json.Unmarshal([]byte(prev), &resp); err == nil {
				log.Info().Str("cmd", cmd.Cmd).Str("request_id", cmd.RequestID).Msg("commander: the same command came again; its first reply sent again, not run twice")
				ch.publishResponse(resp)
				return
			}
			log.Warn().Str("request_id", cmd.RequestID).Msg("commander: the stored reply is unreadable; not run twice")
			return
		}
	}

	log.Info().
		Str("cmd", cmd.Cmd).
		Str("request_id", cmd.RequestID).
		Str("target", cmd.TargetDevice).
		Msg("commander: executing command")

	result, err := handler(cmd)
	if err != nil {
		log.Error().Err(err).Str("cmd", cmd.Cmd).Str("request_id", cmd.RequestID).Msg("commander: command execution failed")
		ch.sendErrorResponse(cmd, err.Error(), claimed)
		return
	}

	ch.sendOkResponse(cmd, result, claimed)
}

// sendOkResponse publishes a successful command response to the Hub, and
// stores it when this invocation holds the command's claim.
func (ch *CommandHandler) sendOkResponse(cmd Command, result json.RawMessage, claimed bool) {
	resp := CommandResponse{
		Protocol:  ProtocolVersion,
		RequestID: cmd.RequestID,
		Cmd:       cmd.Cmd,
		Status:    "ok",
		Result:    result,
		Timestamp: time.Now().UTC(),
	}
	ch.finish(cmd, resp, claimed)
}

// sendErrorResponse publishes an error command response to the Hub, and
// stores it when this invocation holds the command's claim.
func (ch *CommandHandler) sendErrorResponse(cmd Command, errMsg string, claimed bool) {
	resp := CommandResponse{
		Protocol:  ProtocolVersion,
		RequestID: cmd.RequestID,
		Cmd:       cmd.Cmd,
		Status:    "error",
		Error:     errMsg,
		Timestamp: time.Now().UTC(),
	}
	ch.finish(cmd, resp, claimed)
}

// refuse answers a command it did not run, without storing the answer: a
// refusal must never replace the reply of the command that owns the id
// (review 2026-10-18 18:26, FA-R2). [MESHSAT-1584]
func (ch *CommandHandler) refuse(cmd Command, errMsg string) {
	ch.publishResponse(CommandResponse{Protocol: ProtocolVersion, RequestID: cmd.RequestID, Cmd: cmd.Cmd,
		Status: "error", Error: errMsg, Timestamp: time.Now().UTC()})
}

// safeWithoutLog says a command may run when the command log is down: it
// changes nothing, or its admission is keyed by the command itself.
func (ch *CommandHandler) safeWithoutLog(name string) bool {
	switch name {
	case "ping":
		return true
	case "send_text":
		// Either way a send_text goes, its admission must be keyed.
		return ch.deps.SendTextKeyed != nil && (ch.deps.SendTextTo == nil || ch.deps.SendTextToKeyed != nil)
	case "send_mt":
		return ch.deps.SendTextKeyed != nil
	}
	return false
}

// finish stores a command's reply under the claim this invocation holds,
// then publishes it. The log keeps the first reply of the run that claimed
// the id with these arguments.
func (ch *CommandHandler) finish(cmd Command, resp CommandResponse, claimed bool) {
	if claimed && ch.cmdLog != nil && resp.RequestID != "" {
		if data, err := json.Marshal(resp); err == nil {
			if err := ch.cmdLog.FinishHubCommand(resp.RequestID, ch.runID, commandArgsDigest(cmd), string(data)); err != nil {
				log.Warn().Err(err).Str("request_id", resp.RequestID).Msg("commander: reply not stored")
			}
		}
	}
	ch.publishResponse(resp)
}

// publishResponse marshals and publishes a CommandResponse via the HubReporter.
func (ch *CommandHandler) publishResponse(resp CommandResponse) {
	if ch.onResponse != nil {
		ch.onResponse(resp)
	}
	if ch.reporter == nil || !ch.reporter.IsConnected() {
		log.Warn().Str("request_id", resp.RequestID).Msg("commander: cannot publish response (not connected)")
		return
	}

	topic := ch.reporter.topics.BridgeCmdResp(ch.bridgeID)
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
		Address     string `json:"address"`      // e.g. "+31612345678"
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

	// Labelled as MeshSat Android labels a Hub rotation, when the store keeps
	// labels: an sms key rotated this way is that SMS chat's key.
	var localVersion int
	if ls, ok := ch.keyStore.(labelledKeyStore); ok {
		hubVersion := payload.Version
		if hubVersion <= 0 {
			hubVersion = 1 // Android's optInt("version", 1)
		}
		localVersion, err = ls.StoreKeyLabelled(payload.ChannelType, payload.Address, rawKey, fmt.Sprintf("hub-rotated-v%d", hubVersion))
	} else {
		localVersion, err = ch.keyStore.StoreKey(payload.ChannelType, payload.Address, rawKey)
	}
	if err != nil {
		return nil, fmt.Errorf("store key: %w", err)
	}

	// A management key needs a peer to belong to: the address is the peer's
	// alias ("hub"), the key store holds it as mgmt:<alias>, and the OOB
	// service seals and opens frames against that pair. [MESHSAT-964]
	var peerID uint16
	if payload.ChannelType == "mgmt" {
		if ch.mgmtPeer == nil {
			return nil, fmt.Errorf("key_rotate: mgmt keys need the OOB service, which is not enabled on this bridge")
		}
		peerID, err = ch.mgmtPeer(payload.Address, rawKey)
		if err != nil {
			return nil, fmt.Errorf("register mgmt peer %q: %w", payload.Address, err)
		}
		log.Info().Str("alias", payload.Address).Uint16("peer_id", peerID).
			Msg("commander: management peer registered from Hub key_rotate")
	}

	log.Info().
		Str("channel", payload.ChannelType).
		Str("address", payload.Address).
		Int("hub_version", payload.Version).
		Int("local_version", localVersion).
		Msg("commander: channel key rotated from Hub")

	res := map[string]interface{}{
		"applied":       true,
		"local_version": localVersion,
	}
	if payload.ChannelType == "mgmt" {
		res["peer_id"] = peerID
	}
	result, _ := json.Marshal(res)
	return result, nil
}
