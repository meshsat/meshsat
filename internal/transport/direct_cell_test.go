package transport

import "testing"

func TestActToNetworkType(t *testing.T) {
	tests := []struct {
		act  int
		want string
	}{
		{0, "2G"},
		{2, "3G"},
		{3, "2G"},
		{4, "3G"},
		{7, "LTE"},
		{8, "5G"},
		{99, ""},
	}
	for _, tt := range tests {
		got := actToNetworkType(tt.act)
		if got != tt.want {
			t.Errorf("actToNetworkType(%d) = %q, want %q", tt.act, got, tt.want)
		}
	}
}

func TestParseQENG_LTE(t *testing.T) {
	resp := "+QENG: \"servingcell\",\"NOCHANGE\",\"LTE\",\"FDD\",262,01,1A2D003,148,100,1,5,5,9E3F,-109,-13,-80,16,38\r\nOK"
	info := parseQENG(resp)
	if info == nil {
		t.Fatal("parseQENG returned nil")
	}
	if info.NetworkType != "LTE" {
		t.Errorf("NetworkType = %q, want LTE", info.NetworkType)
	}
	if info.MCC != "262" {
		t.Errorf("MCC = %q, want 262", info.MCC)
	}
	if info.MNC != "01" {
		t.Errorf("MNC = %q, want 01", info.MNC)
	}
	if info.CellID != "1A2D003" {
		t.Errorf("CellID = %q, want 1A2D003", info.CellID)
	}
	if info.RSRP == nil || *info.RSRP != -109 {
		t.Errorf("RSRP = %v, want -109", info.RSRP)
	}
	if info.RSRQ == nil || *info.RSRQ != -13 {
		t.Errorf("RSRQ = %v, want -13", info.RSRQ)
	}
}

func TestParseQENG_GSM(t *testing.T) {
	resp := "+QENG: \"servingcell\",\"NOCHANGE\",\"GSM\",262,01,1A2D,9E3F,42\r\nOK"
	info := parseQENG(resp)
	if info == nil {
		t.Fatal("parseQENG returned nil")
	}
	if info.NetworkType != "2G" {
		t.Errorf("NetworkType = %q, want 2G", info.NetworkType)
	}
	if info.MCC != "262" || info.MNC != "01" {
		t.Errorf("MCC/MNC = %s/%s, want 262/01", info.MCC, info.MNC)
	}
}

func TestParseQENG_Invalid(t *testing.T) {
	if info := parseQENG("garbage"); info != nil {
		t.Error("expected nil for garbage input")
	}
	if info := parseQENG("+QENG: too,few"); info != nil {
		t.Error("expected nil for too few fields")
	}
}

func TestParseCREGExtended(t *testing.T) {
	resp := "+CREG: 2,1,\"1A2D\",\"003E9E3F\",7\r\nOK"
	info := parseCREGExtended(resp)
	if info == nil {
		t.Fatal("parseCREGExtended returned nil")
	}
	if info.LAC != "1A2D" {
		t.Errorf("LAC = %q, want 1A2D", info.LAC)
	}
	if info.CellID != "003E9E3F" {
		t.Errorf("CellID = %q, want 003E9E3F", info.CellID)
	}
	if info.NetworkType != "LTE" {
		t.Errorf("NetworkType = %q, want LTE", info.NetworkType)
	}
}

func TestParseCBM(t *testing.T) {
	cbs := parseCBM("+CBM: 100,4370,1,1,1", "EXTREME ALERT: Earthquake detected in your area")
	if cbs == nil {
		t.Fatal("parseCBM returned nil")
	}
	if cbs.SerialNumber != 100 {
		t.Errorf("SerialNumber = %d, want 100", cbs.SerialNumber)
	}
	if cbs.MessageID != 4370 {
		t.Errorf("MessageID = %d, want 4370", cbs.MessageID)
	}
	if cbs.Severity != "extreme" {
		t.Errorf("Severity = %q, want extreme", cbs.Severity)
	}
	if cbs.Text != "EXTREME ALERT: Earthquake detected in your area" {
		t.Errorf("unexpected text: %q", cbs.Text)
	}
}

func TestCbsSeverity(t *testing.T) {
	tests := []struct {
		mid  int
		want string
	}{
		{4370, "extreme"},
		{4380, "severe"},
		{4390, "amber"},
		{4396, "test"},
		{4398, "info"},
		{1000, "unknown"},
	}
	for _, tt := range tests {
		got := cbsSeverity(tt.mid)
		if got != tt.want {
			t.Errorf("cbsSeverity(%d) = %q, want %q", tt.mid, got, tt.want)
		}
	}
}

func TestParseCOPSNetType(t *testing.T) {
	resp := "+COPS: 0,0,\"KPN\",7\r\nOK"
	got := parseCOPSNetType(resp)
	if got != "LTE" {
		t.Errorf("parseCOPSNetType = %q, want LTE", got)
	}
}

func TestParseCREGNetType(t *testing.T) {
	resp := "+CREG: 2,1,\"1A2D\",\"003E9E3F\",7\r\nOK"
	got := parseCREGNetType(resp)
	if got != "LTE" {
		t.Errorf("parseCREGNetType = %q, want LTE", got)
	}
}

func TestParseCOPSNumericPLMN(t *testing.T) {
	tests := []struct {
		resp    string
		wantMCC string
		wantMNC string
	}{
		{"+COPS: 0,2,\"20408\",2\r\nOK", "204", "08"},
		{"+COPS: 0,2,\"310260\",7\r\nOK", "310", "260"},
		{"+COPS: 0,2,\"26201\",2\r\nOK", "262", "01"},
		{"+COPS: 0,0,\"KPN\",7\r\nOK", "", ""}, // not numeric format
		{"+COPS: 0\r\nOK", "", ""},             // not registered
		{"ERROR", "", ""},
	}
	for _, tt := range tests {
		mcc, mnc := parseCOPSNumericPLMN(tt.resp)
		if mcc != tt.wantMCC || mnc != tt.wantMNC {
			t.Errorf("parseCOPSNumericPLMN(%q) = (%q, %q), want (%q, %q)",
				tt.resp, mcc, mnc, tt.wantMCC, tt.wantMNC)
		}
	}
}

func TestParseCREGExtended_NoAcT(t *testing.T) {
	// Huawei E220 returns CREG with only 4 parts (no AcT field)
	resp := "+CREG: 2,1,\"0C1C\",\"D421\"\r\nOK"
	info := parseCREGExtended(resp)
	if info == nil {
		t.Fatal("parseCREGExtended returned nil")
	}
	if info.LAC != "0C1C" {
		t.Errorf("LAC = %q, want 0C1C", info.LAC)
	}
	if info.CellID != "D421" {
		t.Errorf("CellID = %q, want D421", info.CellID)
	}
	if info.NetworkType != "" {
		t.Errorf("NetworkType = %q, want empty (no AcT field)", info.NetworkType)
	}
}
