package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"testing"

	pb "buf.build/gen/go/meshtastic/protobufs/protocolbuffers/go/meshtastic"
	"google.golang.org/protobuf/proto"
)

// A settings write through the API is laid over the node's own copy, field by
// field, and refused in words when it cannot be. [MESHSAT-1405]

// frameCapture is a node stream that keeps what the transport writes.
type frameCapture struct{ bytes.Buffer }

func (c *frameCapture) Read([]byte) (int, error) { return 0, io.EOF }
func (c *frameCapture) Close() error             { return nil }

// admins decodes every frame written as the AdminMessage it carries.
func (c *frameCapture) admins(t *testing.T) []*pb.AdminMessage {
	t.Helper()
	data := c.Bytes()
	var out []*pb.AdminMessage
	for len(data) >= 4 {
		n := int(data[2])<<8 | int(data[3])
		frame := data[4 : 4+n]
		data = data[4+n:]
		tr := &pb.ToRadio{}
		if err := proto.Unmarshal(frame, tr); err != nil {
			t.Fatalf("frame is not a ToRadio: %v", err)
		}
		pkt := tr.GetPacket()
		if pkt.GetDecoded().GetPortnum() != pb.PortNum_ADMIN_APP || pkt.GetTo() != pkt.GetFrom() {
			t.Fatalf("frame is not an admin message to the node itself: %v", pkt)
		}
		admin := &pb.AdminMessage{}
		if err := proto.Unmarshal(pkt.GetDecoded().GetPayload(), admin); err != nil {
			t.Fatalf("payload is not an AdminMessage: %v", err)
		}
		out = append(out, admin)
	}
	return out
}

var testKey = bytes.Repeat([]byte{7}, 32)

// settingsTransport is a transport connected to a node that has sent its
// LoRa, security and position settings, two channels, a module section and
// its own User.
func settingsTransport(t *testing.T) (*DirectMeshTransport, *frameCapture) {
	t.Helper()
	tr := NewDirectMeshTransport("/dev/null")
	capture := &frameCapture{}
	tr.file, tr.connected, tr.myNodeNum, tr.configDone = capture, true, 0x1234abcd, true
	tr.ownUser = &ProtoUser{ID: "!1234abcd", LongName: "Bench", ShortName: "BNCH", IsLicensed: true}
	tr.storeConfig(&pb.Config{PayloadVariant: &pb.Config_Lora{Lora: &pb.Config_LoRaConfig{
		Region: pb.Config_LoRaConfig_EU_868, UsePreset: true, ModemPreset: pb.Config_LoRaConfig_LONG_FAST, HopLimit: 3, TxEnabled: true, TxPower: 27,
	}}})
	tr.storeConfig(&pb.Config{PayloadVariant: &pb.Config_Security{Security: &pb.Config_SecurityConfig{
		PrivateKey: testKey, PublicKey: bytes.Repeat([]byte{9}, 32), SerialEnabled: true,
	}}})
	tr.storeConfig(&pb.Config{PayloadVariant: &pb.Config_Position{Position: &pb.Config_PositionConfig{GpsEnabled: true, PositionBroadcastSecs: 900}}})
	tr.storeModuleConfig(&pb.ModuleConfig{PayloadVariant: &pb.ModuleConfig_Mqtt{Mqtt: &pb.ModuleConfig_MQTTConfig{Address: "mqtt.example", Enabled: true}}})
	tr.storeChannel(&pb.Channel{Index: 0, Role: pb.Channel_PRIMARY, Settings: &pb.ChannelSettings{
		Psk: []byte{1}, Name: "", ModuleSettings: &pb.ModuleSettings{PositionPrecision: 13},
	}})
	tr.storeChannel(&pb.Channel{Index: 1, Role: pb.Channel_SECONDARY, Settings: &pb.ChannelSettings{Psk: bytes.Repeat([]byte{3}, 16), Name: "team"}})
	tr.storeMetadata(&pb.DeviceMetadata{FirmwareVersion: "2.7.26", HasWifi: true, HasBluetooth: true, CanShutdown: true, HwModel: pb.HardwareModel_T_DECK})
	return tr, capture
}

func sentLora(t *testing.T, capture *frameCapture) *pb.Config_LoRaConfig {
	t.Helper()
	admins := capture.admins(t)
	if len(admins) != 1 {
		t.Fatalf("%d admin messages sent, want 1", len(admins))
	}
	lora := admins[0].GetSetConfig().GetLora()
	if lora == nil {
		t.Fatalf("sent %v, want a set_config with the LoRa section", admins[0])
	}
	return lora
}

func TestSetRadioConfig_OnlyTheNamedFieldChanges(t *testing.T) {
	tr, capture := settingsTransport(t)
	if err := tr.SetRadioConfig(context.Background(), "lora", json.RawMessage(`{"hop_limit": 5}`)); err != nil {
		t.Fatalf("SetRadioConfig: %v", err)
	}
	lora := sentLora(t, capture)
	if lora.GetHopLimit() != 5 {
		t.Errorf("hop_limit sent %d, want 5", lora.GetHopLimit())
	}
	// A set_config replaces the section: everything else goes back as the node has it.
	if lora.GetRegion() != pb.Config_LoRaConfig_EU_868 || !lora.GetUsePreset() || !lora.GetTxEnabled() || lora.GetTxPower() != 27 {
		t.Errorf("the rest of the section was not sent back as the node has it: %v", lora)
	}
	// The node does not echo a set: the copy follows what was sent.
	if got := tr.NamedConfig()["config"].(map[string]interface{})["lora"].(map[string]interface{})["hop_limit"]; got != float64(5) {
		t.Errorf("kept hop_limit = %v, want 5", got)
	}
	// The numbered copy the web dashboard reads is refreshed as the config download would write it.
	sent, _ := proto.Marshal(&pb.Config{PayloadVariant: &pb.Config_Lora{Lora: lora}})
	if want := decodeProtoToMap(sent)["6"]; !reflect.DeepEqual(tr.configData["config_6"], want) {
		t.Errorf("numbered copy = %v, want %v", tr.configData["config_6"], want)
	}
}

func TestSetRadioConfig_ZeroValuesNamesAndEnums(t *testing.T) {
	tr, capture := settingsTransport(t)
	// false is written (proto.Merge would skip it), JSON names and enum names are accepted.
	if err := tr.SetRadioConfig(context.Background(), "lora", json.RawMessage(`{"txEnabled": false, "region": "US", "tx_power": 0}`)); err != nil {
		t.Fatalf("SetRadioConfig: %v", err)
	}
	lora := sentLora(t, capture)
	if lora.GetTxEnabled() || lora.GetRegion() != pb.Config_LoRaConfig_US || lora.GetTxPower() != 0 || lora.GetHopLimit() != 3 {
		t.Errorf("sent %v", lora)
	}
}

func TestSetModuleConfig_NumberedFields(t *testing.T) {
	tr, capture := settingsTransport(t)
	// The web dashboard's MQTT form writes the numbered form it reads: 1 enabled, 2 address.
	if err := tr.SetModuleConfig(context.Background(), "mqtt", json.RawMessage(`{"1": true, "2": "broker.example"}`)); err != nil {
		t.Fatalf("SetModuleConfig: %v", err)
	}
	mqtt := capture.admins(t)[0].GetSetModuleConfig().GetMqtt()
	if !mqtt.GetEnabled() || mqtt.GetAddress() != "broker.example" {
		t.Errorf("sent %v", mqtt)
	}
	// Named by number, the keys are still refused.
	var bad *ConfigError
	if err := tr.SetRadioConfig(context.Background(), "security", json.RawMessage(`{"2": "AAAA"}`)); !errors.As(err, &bad) {
		t.Errorf("private_key by its number: err = %v", err)
	}
}

func TestSetRadioConfig_Refusals(t *testing.T) {
	cases := []struct {
		section, body string
		loaded        bool
	}{
		{"lora", `{"hop_limt": 5}`, true},                 // unknown field
		{"lora", `{"hop_limit": 5, "hopLimit": 6}`, true}, // named twice
		{"lora", `{"hop_limit": null}`, true},             // no value
		{"lora", `{"hop_limit": "many"}`, true},           // a value it cannot take
		{"lora", `[1, 2]`, true},                          // not an object
		{"lora", `{}`, true},                              // names nothing
		{"radio", `{"hop_limit": 5}`, true},               // unknown section
		{"sessionkey", `{}`, true},                        // the node's own
		{"device_ui", `{"version": 1}`, true},             // the firmware ignores it
		{"security", `{"private_key": "AAAA"}`, true},     // keys are never changed here
		{"security", `{"public_key": "AAAA"}`, true},
		{"display", `{"screen_on_secs": 30}`, false}, // not sent by the node yet
	}
	for _, c := range cases {
		tr, capture := settingsTransport(t)
		err := tr.SetRadioConfig(context.Background(), c.section, json.RawMessage(c.body))
		var bad *ConfigError
		switch {
		case c.loaded && !errors.As(err, &bad):
			t.Errorf("%s %s: err = %v, want a ConfigError", c.section, c.body, err)
		case !c.loaded && !errors.Is(err, ErrConfigNotLoaded):
			t.Errorf("%s %s: err = %v, want ErrConfigNotLoaded", c.section, c.body, err)
		}
		if capture.Len() != 0 {
			t.Errorf("%s %s: a refused write reached the node", c.section, c.body)
		}
	}
}

func TestSetRadioConfig_SecurityKeepsTheKeys(t *testing.T) {
	tr, capture := settingsTransport(t)
	if err := tr.SetRadioConfig(context.Background(), "security", json.RawMessage(`{"debug_log_api_enabled": true}`)); err != nil {
		t.Fatalf("SetRadioConfig: %v", err)
	}
	sec := capture.admins(t)[0].GetSetConfig().GetSecurity()
	if !sec.GetDebugLogApiEnabled() || !bytes.Equal(sec.GetPrivateKey(), testKey) || !sec.GetSerialEnabled() {
		t.Errorf("sent %v, want the debug log on with the node's own key and settings", sec)
	}
	if on, known := tr.DebugLogSetting(); !on || !known {
		t.Errorf("DebugLogSetting = %v %v after the write", on, known)
	}

	// Without the node's private key, a security write could make it make new keys.
	tr2, capture2 := settingsTransport(t)
	tr2.storeConfig(&pb.Config{PayloadVariant: &pb.Config_Security{Security: &pb.Config_SecurityConfig{SerialEnabled: true}}})
	if err := tr2.SetRadioConfig(context.Background(), "security", json.RawMessage(`{"debug_log_api_enabled": true}`)); !errors.Is(err, ErrConfigNotLoaded) {
		t.Errorf("err = %v, want ErrConfigNotLoaded without the private key", err)
	}
	if capture2.Len() != 0 {
		t.Error("a security write without the node's key reached the node")
	}
}

func TestSetRadioConfig_NotConnected(t *testing.T) {
	tr, _ := settingsTransport(t)
	tr.connected = false
	if err := tr.SetRadioConfig(context.Background(), "lora", json.RawMessage(`{"hop_limit": 4}`)); !errors.Is(err, ErrNotConnected) {
		t.Errorf("err = %v, want ErrNotConnected", err)
	}
}

func TestSetModuleConfig_OverTheNodesOwn(t *testing.T) {
	tr, capture := settingsTransport(t)
	if err := tr.SetModuleConfig(context.Background(), "mqtt", json.RawMessage(`{"enabled": false}`)); err != nil {
		t.Fatalf("SetModuleConfig: %v", err)
	}
	mqtt := capture.admins(t)[0].GetSetModuleConfig().GetMqtt()
	if mqtt.GetEnabled() || mqtt.GetAddress() != "mqtt.example" {
		t.Errorf("sent %v, want enabled false and the address kept", mqtt)
	}
	var bad *ConfigError
	if err := tr.SetModuleConfig(context.Background(), "radio", json.RawMessage(`{}`)); !errors.As(err, &bad) {
		t.Errorf("unknown module section: err = %v", err)
	}
	if err := tr.SetModuleConfig(context.Background(), "tak_config", json.RawMessage(`{"team": 1}`)); !errors.Is(err, ErrConfigNotLoaded) {
		t.Errorf("the old name of the TAK section: err = %v, want ErrConfigNotLoaded (known section, not sent)", err)
	}
}

func TestSetChannel_OverTheNodesOwn(t *testing.T) {
	tr, capture := settingsTransport(t)
	// No psk and no role: the key, the role and the module settings stay.
	if err := tr.SetChannel(context.Background(), ChannelRequest{Index: 0, Name: "msat", UplinkEnabled: true}); err != nil {
		t.Fatalf("SetChannel: %v", err)
	}
	ch := capture.admins(t)[0].GetSetChannel()
	s := ch.GetSettings()
	if ch.GetRole() != pb.Channel_PRIMARY || !bytes.Equal(s.GetPsk(), []byte{1}) || s.GetName() != "msat" || !s.GetUplinkEnabled() ||
		s.GetModuleSettings().GetPositionPrecision() != 13 {
		t.Errorf("sent %v", ch)
	}

	// A key given is written: "AA==" is the one byte 0, no encryption.
	capture.Reset()
	if err := tr.SetChannel(context.Background(), ChannelRequest{Index: 1, Name: "team", Role: "DISABLED", PSK: "AA=="}); err != nil {
		t.Fatalf("SetChannel: %v", err)
	}
	ch = capture.admins(t)[0].GetSetChannel()
	if ch.GetRole() != pb.Channel_DISABLED || !bytes.Equal(ch.GetSettings().GetPsk(), []byte{0}) {
		t.Errorf("sent %v", ch)
	}

	for _, req := range []ChannelRequest{
		{Index: 8},                     // no such channel
		{Index: 0, Role: "SECONDARY"},  // channel 0 is the main channel
		{Index: 1, Role: "PRIMARY"},    // and no other is
		{Index: 1, Role: "SOMETIMES"},  // unknown role
		{Index: 1, PSK: "not base64!"}, // bad key
	} {
		capture.Reset()
		var bad *ConfigError
		if err := tr.SetChannel(context.Background(), req); !errors.As(err, &bad) {
			t.Errorf("%+v: err = %v, want a ConfigError", req, err)
		}
		if capture.Len() != 0 {
			t.Errorf("%+v: a refused channel write reached the node", req)
		}
	}
	if err := tr.SetChannel(context.Background(), ChannelRequest{Index: 5, Role: "SECONDARY"}); !errors.Is(err, ErrConfigNotLoaded) {
		t.Errorf("a channel the node has not sent: err = %v", err)
	}
}

func TestSetOwner_KeepsLicensedMode(t *testing.T) {
	tr, capture := settingsTransport(t)
	if err := tr.SetOwner(context.Background(), "Bench two", "BN2"); err != nil {
		t.Fatalf("SetOwner: %v", err)
	}
	user := capture.admins(t)[0].GetSetOwner()
	if user.GetLongName() != "Bench two" || user.GetShortName() != "BN2" || !user.GetIsLicensed() {
		t.Errorf("sent %v, want the names and is_licensed as the node has it", user)
	}
	tr.ownUser = nil
	if err := tr.SetOwner(context.Background(), "x", "x"); !errors.Is(err, ErrConfigNotLoaded) {
		t.Errorf("err = %v, want ErrConfigNotLoaded before the node sent its own User", err)
	}
}

func TestAdminReplies_ReplaceTheCopy(t *testing.T) {
	tr, _ := settingsTransport(t)
	payload, _ := proto.Marshal(&pb.AdminMessage{PayloadVariant: &pb.AdminMessage_GetConfigResponse{GetConfigResponse: &pb.Config{
		PayloadVariant: &pb.Config_Lora{Lora: &pb.Config_LoRaConfig{Region: pb.Config_LoRaConfig_EU_868, HopLimit: 6, TxEnabled: true}},
	}}})
	tr.storeAdminPayload(payload)
	lora := tr.NamedConfig()["config"].(map[string]interface{})["lora"].(map[string]interface{})
	if lora["hop_limit"] != float64(6) {
		t.Errorf("hop_limit after the node's reply = %v, want 6", lora["hop_limit"])
	}
	payload, _ = proto.Marshal(&pb.AdminMessage{PayloadVariant: &pb.AdminMessage_GetChannelResponse{GetChannelResponse: &pb.Channel{
		Index: 2, Role: pb.Channel_SECONDARY, Settings: &pb.ChannelSettings{Name: "new"},
	}}})
	tr.storeAdminPayload(payload)
	if n := len(tr.NamedConfig()["channels"].([]map[string]interface{})); n != 3 {
		t.Errorf("%d channels after the node's reply, want 3", n)
	}
}

func TestHandshake_KeepsTypedSettings(t *testing.T) {
	tr := NewDirectMeshTransport("/dev/null")
	frame, _ := proto.Marshal(&pb.FromRadio{PayloadVariant: &pb.FromRadio_Config{Config: &pb.Config{
		PayloadVariant: &pb.Config_Bluetooth{Bluetooth: &pb.Config_BluetoothConfig{Enabled: true, Mode: pb.Config_BluetoothConfig_FIXED_PIN, FixedPin: 123456}},
	}}})
	tr.handleFromRadio(frame)
	frame, _ = proto.Marshal(&pb.FromRadio{PayloadVariant: &pb.FromRadio_Metadata{Metadata: &pb.DeviceMetadata{FirmwareVersion: "2.7.26", HasWifi: true}}})
	tr.handleFromRadio(frame)
	named := tr.NamedConfig()
	bt := named["config"].(map[string]interface{})["bluetooth"].(map[string]interface{})
	if bt["enabled"] != true || bt["mode"] != float64(1) || bt["fixed_pin"] != float64(123456) {
		t.Errorf("bluetooth = %v", bt)
	}
	if md := named["metadata"].(map[string]interface{}); md["has_wifi"] != true || md["firmware_version"] != "2.7.26" {
		t.Errorf("metadata = %v", md)
	}
	if _, ok := tr.configData["config_7"]; !ok {
		t.Error("the numbered copy of the bluetooth section is missing")
	}
}

func TestNamedConfig_NoKeys(t *testing.T) {
	tr, _ := settingsTransport(t)
	named := tr.NamedConfig()
	sec := named["config"].(map[string]interface{})["security"].(map[string]interface{})
	if _, ok := sec["private_key"]; ok {
		t.Error("the node's private key is in the named config")
	}
	if sec["private_key_set"] != true {
		t.Errorf("private_key_set = %v", sec["private_key_set"])
	}
	channels := named["channels"].([]map[string]interface{})
	if len(channels) != 2 || channels[0]["key"] != "default" || channels[1]["key"] != "private" || channels[1]["name"] != "team" {
		t.Errorf("channels = %v", channels)
	}
	for _, ch := range channels {
		if _, ok := ch["psk"]; ok {
			t.Errorf("a channel key is in the named config: %v", ch)
		}
	}
	body, _ := json.Marshal(named)
	if bytes.Contains(body, []byte("BwcHBwcHBwcHBwcHBwcHBwcHBwcHBwcHBwcHBwcHBwc")) {
		t.Error("the private key's base64 is somewhere in the named config")
	}
	if named["node_id"] != "!1234abcd" || named["loaded"] != true {
		t.Errorf("node_id %v loaded %v", named["node_id"], named["loaded"])
	}
	owner := named["owner"].(map[string]interface{})
	if owner["long_name"] != "Bench" || owner["is_licensed"] != true {
		t.Errorf("owner = %v", owner)
	}
}

func TestChannelKeyWord(t *testing.T) {
	cases := []struct {
		psk  []byte
		role pb.Channel_Role
		want string
	}{
		{nil, pb.Channel_SECONDARY, "main"},
		{nil, pb.Channel_PRIMARY, "none"},
		{[]byte{0}, pb.Channel_PRIMARY, "none"},
		{[]byte{1}, pb.Channel_PRIMARY, "default"},
		{[]byte{5}, pb.Channel_SECONDARY, "default"},
		{bytes.Repeat([]byte{1}, 16), pb.Channel_PRIMARY, "private"},
		{bytes.Repeat([]byte{1}, 32), pb.Channel_SECONDARY, "private"},
	}
	for _, c := range cases {
		if got := channelKeyWord(c.psk, c.role); got != c.want {
			t.Errorf("channelKeyWord(%v, %v) = %q, want %q", c.psk, c.role, got, c.want)
		}
	}
}

func TestNodeAdmin_ClockSwitchOffForget(t *testing.T) {
	tr, capture := settingsTransport(t)
	if err := tr.AdminSetClock(); err != nil {
		t.Fatal(err)
	}
	if err := tr.AdminShutdown(0); err != nil {
		t.Fatal(err)
	}
	if err := tr.AdminForgetNodes(); err != nil {
		t.Fatal(err)
	}
	admins := capture.admins(t)
	if len(admins) != 3 || admins[0].GetSetTimeOnly() < 1790000000 || admins[1].GetShutdownSeconds() != 5 || !admins[2].GetNodedbReset() {
		t.Errorf("sent %v", admins)
	}
	// A clock nobody set is not written into the node.
	capture.Reset()
	tr.SetClockTrustFn(func() bool { return false })
	if err := tr.AdminSetClock(); !errors.Is(err, ErrClockUntrusted) {
		t.Errorf("err = %v, want ErrClockUntrusted", err)
	}
	if capture.Len() != 0 {
		t.Error("an untrusted clock reached the node")
	}
}
