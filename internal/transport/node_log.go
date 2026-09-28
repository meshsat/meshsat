package transport

import (
	pb "buf.build/gen/go/meshtastic/protobufs/protocolbuffers/go/meshtastic"
	"google.golang.org/protobuf/proto"
)

// recordLogRadio keeps one value the node's LogRadio characteristic
// notified: a LogRecord, as a line of the radio log. [MESHSAT-1406]
func (t *DirectMeshTransport) recordLogRadio(value []byte) {
	rec := &pb.LogRecord{}
	if proto.Unmarshal(value, rec) != nil || rec.GetMessage() == "" {
		return
	}
	level := ""
	if rec.GetLevel() != pb.LogRecord_UNSET {
		level = rec.GetLevel().String()
	}
	t.addRadioLog(&ProtoLogRecord{Message: rec.GetMessage(), Time: rec.GetTime(), Source: rec.GetSource(), Level: level}, false)
}

// DebugLogSetting is the node's security.debug_log_api_enabled as the node
// reported it; known is false until the node has sent its security
// settings. [MESHSAT-1406]
func (t *DirectMeshTransport) DebugLogSetting() (enabled, known bool) {
	t.configMu.RLock()
	defer t.configMu.RUnlock()
	sec, ok := t.settings.config["security"].(*pb.Config_SecurityConfig)
	if !ok {
		return false, false
	}
	return sec.GetDebugLogApiEnabled(), true
}
