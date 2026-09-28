package transport

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"time"

	pb "buf.build/gen/go/meshtastic/protobufs/protocolbuffers/go/meshtastic"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// The node's settings as the node itself reported them, kept as protobuf
// messages. A Meshtastic set_config replaces a whole section, so a write
// through the API is laid over the node's own copy field by field: a request
// that names only hop_limit must not send the region back as zero, which
// stops the node transmitting. [MESHSAT-1405]

// ConfigError is a settings write the API refuses as it stands (HTTP 400).
type ConfigError struct{ Msg string }

func (e *ConfigError) Error() string { return e.Msg }

// ErrConfigNotLoaded means the node has not sent the settings a write would
// be laid over (HTTP 409): writing without them would send the rest as zero.
var ErrConfigNotLoaded = errors.New("the node has not sent these settings yet")

// nodeSettings is the typed copy of the node's settings. Guarded by the
// transport's configMu.
type nodeSettings struct {
	config   map[string]proto.Message // "lora" -> *pb.Config_LoRaConfig
	module   map[string]proto.Message // "mqtt" -> *pb.ModuleConfig_MQTTConfig
	channels map[int32]*pb.Channel
	metadata *pb.DeviceMetadata
}

// moduleAliases are the names the API used before the sections were read
// from the protobuf descriptors.
var moduleAliases = map[string]string{"status_message": "statusmessage", "tak_config": "tak"}

// unwritableSections are Config sections a client never sets: the session
// key is the node's own, and the firmware ignores a device_ui set.
var unwritableSections = map[string]bool{"sessionkey": true, "device_ui": true}

// sectionField returns the oneof field of wrapper (pb.Config or
// pb.ModuleConfig) that carries the named section.
func sectionField(wrapper proto.Message, name string) protoreflect.FieldDescriptor {
	fd := wrapper.ProtoReflect().Descriptor().Fields().ByName(protoreflect.Name(name))
	if fd == nil || fd.ContainingOneof() == nil || fd.Message() == nil {
		return nil
	}
	return fd
}

// whichSection returns the section a pb.Config or pb.ModuleConfig carries.
func whichSection(wrapper proto.Message) (string, proto.Message) {
	m := wrapper.ProtoReflect()
	oneofs := m.Descriptor().Oneofs()
	for i := 0; i < oneofs.Len(); i++ {
		if fd := m.WhichOneof(oneofs.Get(i)); fd != nil && fd.Message() != nil {
			return string(fd.Name()), m.Get(fd).Message().Interface()
		}
	}
	return "", nil
}

func (t *DirectMeshTransport) settingsLocked() *nodeSettings {
	if t.settings.config == nil {
		t.settings.config = make(map[string]proto.Message)
		t.settings.module = make(map[string]proto.Message)
		t.settings.channels = make(map[int32]*pb.Channel)
	}
	return &t.settings
}

// storeConfig keeps one Config section, typed and in the numbered form the
// web dashboard reads (config_<field number>).
func (t *DirectMeshTransport) storeConfig(cfg *pb.Config) {
	name, msg := whichSection(cfg)
	if name == "" {
		return
	}
	raw, _ := proto.Marshal(cfg)
	decoded := decodeProtoToMap(raw)
	t.configMu.Lock()
	defer t.configMu.Unlock()
	t.settingsLocked().config[name] = proto.Clone(msg)
	for k, v := range decoded {
		t.configData["config_"+k] = v
	}
}

// storeModuleConfig keeps one ModuleConfig section (module_<field number>).
func (t *DirectMeshTransport) storeModuleConfig(mc *pb.ModuleConfig) {
	name, msg := whichSection(mc)
	if name == "" {
		return
	}
	raw, _ := proto.Marshal(mc)
	decoded := decodeProtoToMap(raw)
	t.configMu.Lock()
	defer t.configMu.Unlock()
	t.settingsLocked().module[name] = proto.Clone(msg)
	for k, v := range decoded {
		t.configData["module_"+k] = v
	}
}

// storeChannel keeps one channel (channel_<index>).
func (t *DirectMeshTransport) storeChannel(ch *pb.Channel) {
	raw, _ := proto.Marshal(ch)
	decoded := decodeProtoToMap(raw)
	t.configMu.Lock()
	defer t.configMu.Unlock()
	t.settingsLocked().channels[ch.GetIndex()] = proto.Clone(ch).(*pb.Channel)
	t.configData[fmt.Sprintf("channel_%d", ch.GetIndex())] = decoded
}

func (t *DirectMeshTransport) storeMetadata(md *pb.DeviceMetadata) {
	if md == nil {
		return
	}
	t.configMu.Lock()
	defer t.configMu.Unlock()
	t.settingsLocked().metadata = proto.Clone(md).(*pb.DeviceMetadata)
}

// storeFromRadio keeps the settings a FromRadio of the config download
// carries.
func (t *DirectMeshTransport) storeFromRadio(fr *ProtoFromRadio) {
	if fr.ConfigRaw != nil {
		cfg := &pb.Config{}
		if proto.Unmarshal(fr.ConfigRaw, cfg) == nil {
			t.storeConfig(cfg)
		}
	}
	if fr.ModuleConfigRaw != nil {
		mc := &pb.ModuleConfig{}
		if proto.Unmarshal(fr.ModuleConfigRaw, mc) == nil {
			t.storeModuleConfig(mc)
		}
	}
	if fr.ChannelRaw != nil {
		ch := &pb.Channel{}
		if proto.Unmarshal(fr.ChannelRaw, ch) == nil {
			t.storeChannel(ch)
		}
	}
	if fr.Metadata != nil {
		t.storeMetadata(fr.Metadata)
	}
}

// storeAdminPayload keeps what an admin reply from the node itself carries.
func (t *DirectMeshTransport) storeAdminPayload(payload []byte) {
	admin := &pb.AdminMessage{}
	if proto.Unmarshal(payload, admin) == nil {
		t.storeAdminReply(admin)
	}
}

// storeAdminReply keeps what an admin reply from the node itself carries:
// the answer to GET /api/config/{section} and the other get requests.
func (t *DirectMeshTransport) storeAdminReply(admin *pb.AdminMessage) {
	switch v := admin.GetPayloadVariant().(type) {
	case *pb.AdminMessage_GetConfigResponse:
		if v.GetConfigResponse != nil {
			t.storeConfig(v.GetConfigResponse)
		}
	case *pb.AdminMessage_GetModuleConfigResponse:
		if v.GetModuleConfigResponse != nil {
			t.storeModuleConfig(v.GetModuleConfigResponse)
		}
	case *pb.AdminMessage_GetChannelResponse:
		if v.GetChannelResponse != nil {
			t.storeChannel(v.GetChannelResponse)
		}
	case *pb.AdminMessage_GetOwnerResponse:
		if u := v.GetOwnerResponse; u != nil && (u.GetLongName() != "" || u.GetShortName() != "") {
			t.mu.Lock()
			if t.ownUser != nil {
				own := *t.ownUser
				own.LongName, own.ShortName, own.IsLicensed = u.GetLongName(), u.GetShortName(), u.GetIsLicensed()
				t.ownUser = &own
			}
			t.mu.Unlock()
		}
	case *pb.AdminMessage_GetDeviceMetadataResponse:
		t.storeMetadata(v.GetDeviceMetadataResponse)
	}
}

// mergeSettings lays the fields of a JSON object over a copy of current:
// only the named fields change, and a named field set to its zero value is
// written as zero (proto.Merge would skip it). Fields are named by their
// proto names, their JSON names or their numbers; enums are numbers or
// names.
func mergeSettings(section string, current proto.Message, patch json.RawMessage) (proto.Message, []protoreflect.FieldDescriptor, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(patch, &fields); err != nil || fields == nil {
		return nil, nil, &ConfigError{Msg: "config must be a JSON object of the " + section + " settings"}
	}
	if len(fields) == 0 {
		return nil, nil, &ConfigError{Msg: "config names no " + section + " setting"}
	}
	md := current.ProtoReflect().Descriptor()
	named := make(map[string]json.RawMessage, len(fields))
	fds := make([]protoreflect.FieldDescriptor, 0, len(fields))
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fd := md.Fields().ByName(protoreflect.Name(k))
		if fd == nil {
			fd = md.Fields().ByJSONName(k)
		}
		if n, err := strconv.Atoi(k); fd == nil && err == nil && n > 0 {
			// The numbered form GET /api/config gives (the web dashboard's forms).
			fd = md.Fields().ByNumber(protoreflect.FieldNumber(n))
		}
		if fd == nil {
			return nil, nil, &ConfigError{Msg: fmt.Sprintf("unknown %s setting %q", section, k)}
		}
		name := string(fd.Name())
		if _, dup := named[name]; dup {
			return nil, nil, &ConfigError{Msg: fmt.Sprintf("%s setting %q is named twice", section, name)}
		}
		if bytes.Equal(bytes.TrimSpace(fields[k]), []byte("null")) {
			return nil, nil, &ConfigError{Msg: fmt.Sprintf("%s setting %q needs a value", section, name)}
		}
		named[name] = fields[k]
		fds = append(fds, fd)
	}
	body, _ := json.Marshal(named)
	src := current.ProtoReflect().New().Interface()
	if err := protojson.Unmarshal(body, src); err != nil {
		return nil, nil, &ConfigError{Msg: fmt.Sprintf("a %s setting has a value it cannot take: %v", section, err)}
	}
	dst := proto.Clone(current)
	for _, fd := range fds {
		if src.ProtoReflect().Has(fd) {
			dst.ProtoReflect().Set(fd, src.ProtoReflect().Get(fd))
		} else {
			dst.ProtoReflect().Clear(fd)
		}
	}
	return dst, fds, nil
}

// securityGuard refuses a security write that could make the node make a
// new key pair: a new key changes the node's number and orphans it on the
// mesh, and older firmware makes one whenever a set carries no private key.
func securityGuard(current proto.Message, fds []protoreflect.FieldDescriptor) error {
	for _, fd := range fds {
		if fd.Name() == "private_key" || fd.Name() == "public_key" {
			return &ConfigError{Msg: "the node's keys are not changed through the settings; only its other security settings are"}
		}
	}
	sec, ok := current.(*pb.Config_SecurityConfig)
	if !ok || len(sec.GetPrivateKey()) != 32 {
		return fmt.Errorf("%w: the node has not sent its private key, and a security write without it could make the node make new keys", ErrConfigNotLoaded)
	}
	return nil
}

// sendAdmin sends an AdminMessage to the node itself.
func (t *DirectMeshTransport) sendAdmin(admin *pb.AdminMessage) error {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if !t.connected || t.file == nil || t.myNodeNum == 0 {
		return ErrNotConnected
	}
	frame := buildAdminToRadioMsg(t.myNodeNum, t.myNodeNum, admin)
	if frame == nil {
		return errors.New("could not encode the admin message")
	}
	return sendFrame(t.file, frame)
}

// SetRadioConfig writes one Config section: the node's own copy with the
// fields of data laid over it. [MESHSAT-1405]
func (t *DirectMeshTransport) SetRadioConfig(_ context.Context, section string, data json.RawMessage) error {
	t.settingsWriteMu.Lock()
	defer t.settingsWriteMu.Unlock()
	fd := sectionField(&pb.Config{}, section)
	if fd == nil || unwritableSections[section] {
		return &ConfigError{Msg: "unknown config section: " + section}
	}
	t.configMu.RLock()
	current := t.settings.config[section]
	t.configMu.RUnlock()
	if current == nil {
		return fmt.Errorf("%w: %s", ErrConfigNotLoaded, section)
	}
	merged, fds, err := mergeSettings(section, current, data)
	if err != nil {
		return err
	}
	if section == "security" {
		if err := securityGuard(current, fds); err != nil {
			return err
		}
	}
	cfg := &pb.Config{}
	cfg.ProtoReflect().Set(fd, protoreflect.ValueOfMessage(merged.ProtoReflect()))
	if err := t.sendAdmin(&pb.AdminMessage{PayloadVariant: &pb.AdminMessage_SetConfig{SetConfig: cfg}}); err != nil {
		return err
	}
	// The node does not echo a set: keep the copy in step with what was
	// sent. A read-back (GET /api/config/{section}) replaces it with what
	// the node holds.
	t.storeConfig(cfg)
	return nil
}

// SetModuleConfig writes one ModuleConfig section the same way.
func (t *DirectMeshTransport) SetModuleConfig(_ context.Context, section string, data json.RawMessage) error {
	t.settingsWriteMu.Lock()
	defer t.settingsWriteMu.Unlock()
	if alias, ok := moduleAliases[section]; ok {
		section = alias
	}
	fd := sectionField(&pb.ModuleConfig{}, section)
	if fd == nil {
		return &ConfigError{Msg: "unknown module config section: " + section}
	}
	t.configMu.RLock()
	current := t.settings.module[section]
	t.configMu.RUnlock()
	if current == nil {
		return fmt.Errorf("%w: %s", ErrConfigNotLoaded, section)
	}
	merged, _, err := mergeSettings(section, current, data)
	if err != nil {
		return err
	}
	mc := &pb.ModuleConfig{}
	mc.ProtoReflect().Set(fd, protoreflect.ValueOfMessage(merged.ProtoReflect()))
	if err := t.sendAdmin(&pb.AdminMessage{PayloadVariant: &pb.AdminMessage_SetModuleConfig{SetModuleConfig: mc}}); err != nil {
		return err
	}
	t.storeModuleConfig(mc)
	return nil
}

// ErrClockUntrusted means this computer's clock is not known to be right, so
// it is not written into the node (HTTP 409). [MESHSAT-1056]
var ErrClockUntrusted = errors.New("this computer's clock is not set yet, so the node's clock is left as it is")

// AdminSetClock sets the node's clock to this computer's (Android's "Set
// the clock"). [MESHSAT-1405]
func (t *DirectMeshTransport) AdminSetClock() error {
	if !t.clockTrustedForRadio() {
		return ErrClockUntrusted
	}
	return t.sendAdmin(&pb.AdminMessage{PayloadVariant: &pb.AdminMessage_SetTimeOnly{SetTimeOnly: uint32(time.Now().Unix())}})
}

// AdminShutdown switches the node off after delay seconds; it stays off
// until someone switches it on at the node.
func (t *DirectMeshTransport) AdminShutdown(delay int) error {
	if delay <= 0 {
		delay = 5
	}
	return t.sendAdmin(&pb.AdminMessage{PayloadVariant: &pb.AdminMessage_ShutdownSeconds{ShutdownSeconds: int32(delay)}})
}

// AdminForgetNodes clears the node's list of the nodes it has heard (they
// come back as they transmit again); the node keeps its favourites.
func (t *DirectMeshTransport) AdminForgetNodes() error {
	return t.sendAdmin(&pb.AdminMessage{PayloadVariant: &pb.AdminMessage_NodedbReset{NodedbReset: true}})
}

// channelRoles are the roles ChannelRequest names.
var channelRoles = map[string]pb.Channel_Role{"PRIMARY": pb.Channel_PRIMARY, "SECONDARY": pb.Channel_SECONDARY, "DISABLED": pb.Channel_DISABLED}

// SetChannel writes one channel over the node's own: name and the MQTT
// switches as given, the key only when one is given (a request without
// psk used to clear it, so the channel stopped being encrypted), the role
// kept when none is given. [MESHSAT-1405]
func (t *DirectMeshTransport) SetChannel(_ context.Context, req ChannelRequest) error {
	t.settingsWriteMu.Lock()
	defer t.settingsWriteMu.Unlock()
	if req.Index > 7 {
		return &ConfigError{Msg: fmt.Sprintf("there is no channel %d: channels are 0 to 7", req.Index)}
	}
	t.configMu.RLock()
	current := t.settings.channels[int32(req.Index)]
	t.configMu.RUnlock()
	if current == nil {
		return fmt.Errorf("%w: channel %d", ErrConfigNotLoaded, req.Index)
	}
	ch := proto.Clone(current).(*pb.Channel)
	if req.Role != "" {
		role, ok := channelRoles[req.Role]
		if !ok {
			return &ConfigError{Msg: "unknown channel role: " + req.Role}
		}
		ch.Role = role
	}
	if req.Index == 0 && ch.GetRole() != pb.Channel_PRIMARY {
		return &ConfigError{Msg: "channel 0 is always the main channel"}
	}
	if req.Index != 0 && ch.GetRole() == pb.Channel_PRIMARY {
		return &ConfigError{Msg: "only channel 0 is the main channel"}
	}
	if ch.Settings == nil {
		ch.Settings = &pb.ChannelSettings{}
	}
	ch.Settings.Name = req.Name
	ch.Settings.UplinkEnabled = req.UplinkEnabled
	ch.Settings.DownlinkEnabled = req.DownlinkEnabled
	if req.PSK != "" {
		psk, err := base64.StdEncoding.DecodeString(req.PSK)
		if err != nil {
			return &ConfigError{Msg: "psk is not base64: " + err.Error()}
		}
		ch.Settings.Psk = psk
	}
	if err := t.sendAdmin(&pb.AdminMessage{PayloadVariant: &pb.AdminMessage_SetChannel{SetChannel: ch}}); err != nil {
		return err
	}
	t.storeChannel(ch)
	return nil
}

// SetOwner sets the node's long and short name. is_licensed goes back as
// the node reported it: false would switch a licensed (ham) node out of
// licensed mode, and the node would make new keys. [MESHSAT-1405]
func (t *DirectMeshTransport) SetOwner(_ context.Context, longName, shortName string) error {
	t.mu.RLock()
	own := t.ownUser
	t.mu.RUnlock()
	if own == nil {
		return fmt.Errorf("%w: the node's own name", ErrConfigNotLoaded)
	}
	user := &pb.User{LongName: longName, ShortName: shortName, IsLicensed: own.IsLicensed}
	if err := t.sendAdmin(&pb.AdminMessage{PayloadVariant: &pb.AdminMessage_SetOwner{SetOwner: user}}); err != nil {
		return err
	}
	// The NodeInfo requests carry the new name from now on, not the one of
	// the last config download. [MESHSAT-1388]
	t.mu.Lock()
	if t.ownUser != nil {
		next := *t.ownUser
		if longName != "" {
			next.LongName = longName
		}
		if shortName != "" {
			next.ShortName = shortName
		}
		t.ownUser = &next
	}
	t.mu.Unlock()
	return nil
}

// channelKeyWord says what a channel's key means without giving it away:
// "main" (an extra channel on the main channel's key), "none" (not
// encrypted), "default" (the key every Meshtastic radio knows) or
// "private". MeshSat Android's RadioConfigScreen channelKey words.
func channelKeyWord(psk []byte, role pb.Channel_Role) string {
	switch {
	case len(psk) == 0 && role == pb.Channel_SECONDARY:
		return "main"
	case len(psk) == 0 || (len(psk) == 1 && psk[0] == 0):
		return "none"
	case len(psk) == 1:
		return "default"
	default:
		return "private"
	}
}

var namedJSON = protojson.MarshalOptions{UseProtoNames: true, EmitUnpopulated: true, UseEnumNumbers: true}

func namedMessage(m proto.Message) map[string]interface{} {
	body, err := namedJSON.Marshal(m)
	if err != nil {
		return map[string]interface{}{}
	}
	out := map[string]interface{}{}
	_ = json.Unmarshal(body, &out)
	return out
}

// NamedConfig is the node's settings with section and field names (GET
// /api/config?format=names): what the apps' radio settings pages read.
// Channel keys and the node's private key are never in it. [MESHSAT-1405]
func (t *DirectMeshTransport) NamedConfig() map[string]interface{} {
	t.mu.RLock()
	num, loaded, firmware := t.myNodeNum, t.configDone, t.firmwareVer
	var owner map[string]interface{}
	if t.ownUser != nil {
		owner = map[string]interface{}{"long_name": t.ownUser.LongName, "short_name": t.ownUser.ShortName, "is_licensed": t.ownUser.IsLicensed,
			"hw_model": t.ownUser.HWModel}
	}
	t.mu.RUnlock()

	t.configMu.RLock()
	defer t.configMu.RUnlock()
	config := map[string]interface{}{}
	for name, msg := range t.settings.config {
		if name == "sessionkey" {
			continue
		}
		if sec, ok := msg.(*pb.Config_SecurityConfig); ok {
			shown := proto.Clone(sec).(*pb.Config_SecurityConfig)
			shown.PrivateKey = nil
			fields := namedMessage(shown)
			delete(fields, "private_key")
			fields["private_key_set"] = len(sec.GetPrivateKey()) == 32
			config[name] = fields
			continue
		}
		config[name] = namedMessage(msg)
	}
	module := map[string]interface{}{}
	for name, msg := range t.settings.module {
		module[name] = namedMessage(msg)
	}
	indexes := make([]int, 0, len(t.settings.channels))
	for i := range t.settings.channels {
		indexes = append(indexes, int(i))
	}
	sort.Ints(indexes)
	channels := make([]map[string]interface{}, 0, len(indexes))
	for _, i := range indexes {
		ch := t.settings.channels[int32(i)]
		s := ch.GetSettings()
		entry := map[string]interface{}{
			"index": ch.GetIndex(), "role": int32(ch.GetRole()), "name": s.GetName(), "key": channelKeyWord(s.GetPsk(), ch.GetRole()),
			"uplink_enabled": s.GetUplinkEnabled(), "downlink_enabled": s.GetDownlinkEnabled(),
		}
		if ms := s.GetModuleSettings(); ms != nil {
			entry["position_precision"] = ms.GetPositionPrecision()
		}
		channels = append(channels, entry)
	}
	var metadata map[string]interface{}
	if md := t.settings.metadata; md != nil {
		// DeviceMetadata's own field names are camelCase (hasWifi); these
		// are snake_case like every other name here.
		metadata = map[string]interface{}{
			"firmware_version": md.GetFirmwareVersion(), "device_state_version": md.GetDeviceStateVersion(), "hw_model": int32(md.GetHwModel()),
			"role": int32(md.GetRole()), "position_flags": md.GetPositionFlags(), "can_shutdown": md.GetCanShutdown(), "has_wifi": md.GetHasWifi(),
			"has_bluetooth": md.GetHasBluetooth(), "has_ethernet": md.GetHasEthernet(), "has_remote_hardware": md.GetHasRemoteHardware(),
			"has_pkc": md.GetHasPKC(),
		}
	}
	nodeID := ""
	if num != 0 {
		nodeID = fmt.Sprintf("!%08x", num)
	}
	return map[string]interface{}{
		"loaded": loaded, "node_num": num, "node_id": nodeID, "firmware_version": firmware,
		"owner": owner, "metadata": metadata, "config": config, "module": module, "channels": channels,
	}
}
