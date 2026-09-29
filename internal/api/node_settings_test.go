package api

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"meshsat/internal/engine"
	"meshsat/internal/transport"
)

// The node's settings through the API answer why a write cannot go, the
// named view is served, the radio log is read by sequence, and a CA
// certificate keeps its facts. [MESHSAT-1405, MESHSAT-1406, MESHSAT-1404]

type settingsMesh struct {
	transport.MeshTransport
	err      error
	named    map[string]interface{}
	followed int
	lines    []transport.RadioLogLine
}

func (m *settingsMesh) SetRadioConfig(context.Context, string, json.RawMessage) error  { return m.err }
func (m *settingsMesh) SetModuleConfig(context.Context, string, json.RawMessage) error { return m.err }
func (m *settingsMesh) SetChannel(context.Context, transport.ChannelRequest) error     { return m.err }
func (m *settingsMesh) SetOwner(context.Context, string, string) error                 { return m.err }
func (m *settingsMesh) GetStatus(context.Context) (*transport.MeshStatus, error) {
	return &transport.MeshStatus{}, nil
}
func (m *settingsMesh) NamedConfig() map[string]interface{} { return m.named }
func (m *settingsMesh) RadioLog(int) []transport.RadioLogLine {
	return m.lines
}
func (m *settingsMesh) RadioLogAfter(after uint64) []transport.RadioLogLine {
	var out []transport.RadioLogLine
	for _, l := range m.lines {
		if l.Seq > after {
			out = append(out, l)
		}
	}
	return out
}
func (m *settingsMesh) FollowNodeLog() (bool, bool) { m.followed++; return true, true }
func (m *settingsMesh) NodeLogStatus() (bool, bool) { return true, false }
func (m *settingsMesh) DebugLogSetting() (bool, bool) {
	return true, true
}

func serve(t *testing.T, s *Server, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.Router().ServeHTTP(w, req)
	return w
}

func TestSettingsWrites_AnswerWhy(t *testing.T) {
	cases := []struct {
		err  error
		want int
	}{
		{nil, http.StatusOK},
		{&transport.ConfigError{Msg: `unknown lora setting "hop_limt"`}, http.StatusBadRequest},
		{fmt.Errorf("%w: lora", transport.ErrConfigNotLoaded), http.StatusConflict},
		{transport.ErrNotConnected, http.StatusServiceUnavailable},
		{errors.New("write: broken pipe"), http.StatusInternalServerError},
	}
	writes := []struct{ path, body string }{
		{"/api/config/radio", `{"section":"lora","config":{"hop_limit":4}}`},
		{"/api/config/module", `{"section":"mqtt","config":{"enabled":false}}`},
		{"/api/channels", `{"index":1,"name":"team"}`},
		{"/api/config/owner", `{"long_name":"Bench","short_name":"BNCH"}`},
	}
	for _, c := range cases {
		s := &Server{mesh: &settingsMesh{err: c.err}}
		for _, wr := range writes {
			w := serve(t, s, "POST", wr.path, wr.body)
			if w.Code != c.want {
				t.Errorf("%s with %v: %d %s, want %d", wr.path, c.err, w.Code, w.Body.String(), c.want)
			}
			var bad *transport.ConfigError
			if errors.As(c.err, &bad) && !strings.Contains(w.Body.String(), "hop_limt") {
				t.Errorf("%s: the reason is not in the answer: %s", wr.path, w.Body.String())
			}
		}
	}
}

type sectionRecorder struct {
	settingsMesh
	section string
	config  string
}

func (m *sectionRecorder) SetRadioConfig(_ context.Context, section string, config json.RawMessage) error {
	m.section, m.config = section, string(config)
	return nil
}

func TestSetRadioConfig_BothShapes(t *testing.T) {
	mesh := &sectionRecorder{}
	s := &Server{mesh: mesh}
	serve(t, s, "POST", "/api/config/radio", `{"section":"lora","config":{"hop_limit":4}}`)
	if mesh.section != "lora" || mesh.config != `{"hop_limit":4}` {
		t.Errorf("with config: %q %q", mesh.section, mesh.config)
	}
	serve(t, s, "POST", "/api/config/radio", `{"section":"lora","hop_limit":5}`)
	if mesh.section != "lora" || mesh.config != `{"hop_limit":5}` {
		t.Errorf("fields beside section: %q %q", mesh.section, mesh.config)
	}
	if w := serve(t, s, "POST", "/api/config/radio", `{"config":{"hop_limit":4}}`); w.Code != http.StatusBadRequest {
		t.Errorf("no section: %d", w.Code)
	}
	if w := serve(t, s, "POST", "/api/config/radio", `[1]`); w.Code != http.StatusBadRequest {
		t.Errorf("not an object: %d", w.Code)
	}
}

func TestGetConfig_Named(t *testing.T) {
	s := &Server{mesh: &settingsMesh{named: map[string]interface{}{"loaded": true, "config": map[string]interface{}{"lora": map[string]interface{}{"hop_limit": 3}}}}}
	w := serve(t, s, "GET", "/api/config?format=names", "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"hop_limit":3`) {
		t.Errorf("format=names: %d %s", w.Code, w.Body.String())
	}
	if w := serve(t, s, "GET", "/api/config?format=pretty", ""); w.Code != http.StatusBadRequest {
		t.Errorf("an unknown format: %d", w.Code)
	}
	type plain struct{ transport.MeshTransport }
	s = &Server{mesh: plain{}}
	if w := serve(t, s, "GET", "/api/config?format=names", ""); w.Code != http.StatusServiceUnavailable {
		t.Errorf("a transport without names: %d", w.Code)
	}
}

func TestRadioLog_AfterAndFollow(t *testing.T) {
	mesh := &settingsMesh{lines: []transport.RadioLogLine{{Seq: 1, Message: "a"}, {Seq: 2, Message: "b"}, {Seq: 3, Message: "c"}}}
	s := &Server{mesh: mesh}
	var answer struct {
		Count     int                      `json:"count"`
		Lines     []transport.RadioLogLine `json:"lines"`
		Available bool                     `json:"available"`
		Following bool                     `json:"following"`
		Debug     *bool                    `json:"debug_log_api_enabled"`
	}
	w := serve(t, s, "GET", "/api/mesh/radio-log?after=1&follow=1", "")
	if err := json.Unmarshal(w.Body.Bytes(), &answer); err != nil || w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	if answer.Count != 2 || answer.Lines[0].Seq != 2 || !answer.Available || !answer.Following || answer.Debug == nil || !*answer.Debug || mesh.followed != 1 {
		t.Errorf("answer %+v, followed %d", answer, mesh.followed)
	}
	w = serve(t, s, "GET", "/api/mesh/radio-log", "")
	answer.Following = true
	_ = json.Unmarshal(w.Body.Bytes(), &answer)
	if answer.Count != 3 || answer.Following || mesh.followed != 1 {
		t.Errorf("without follow: %+v, followed %d", answer, mesh.followed)
	}
	if w := serve(t, s, "GET", "/api/mesh/radio-log?after=x", ""); w.Code != http.StatusBadRequest {
		t.Errorf("after=x: %d", w.Code)
	}
}

// certPEM makes a certificate for the classifier: a CA signs itself, a leaf
// is signed by the CA.
func certPEM(t *testing.T, cn string, isCA bool, parent *x509.Certificate, parentKey *ecdsa.PrivateKey) ([]byte, *x509.Certificate, *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: cn},
		NotBefore: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), NotAfter: time.Date(2031, 9, 1, 12, 0, 0, 0, time.UTC),
		IsCA: isCA, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
	}
	if parent == nil {
		parent, parentKey = tmpl, key
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, parent, &key.PublicKey, parentKey)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), cert, key
}

func TestClassifyPEMs_ACertificateAuthorityKeepsItsFacts(t *testing.T) {
	caPEM, ca, caKey := certPEM(t, "Hub CA", true, nil, nil)
	caSum := sha256.Sum256(ca.Raw)
	bundle, err := classifyPEMs(map[string][]byte{"ca.pem": caPEM})
	if err != nil {
		t.Fatal(err)
	}
	if bundle.subject != "Hub CA" || bundle.notAfter != "2031-09-01 12:00:00" || bundle.fingerprint != hex.EncodeToString(caSum[:]) {
		t.Errorf("a CA alone: subject %q, not after %q, fingerprint %q", bundle.subject, bundle.notAfter, bundle.fingerprint)
	}

	leafPEM, leaf, _ := certPEM(t, "bridge.meshsat.test", false, ca, caKey)
	leafSum := sha256.Sum256(leaf.Raw)
	bundle, err = classifyPEMs(map[string][]byte{"bundle.pem": append(append([]byte{}, caPEM...), leafPEM...)})
	if err != nil {
		t.Fatal(err)
	}
	if bundle.subject != "bridge.meshsat.test" || bundle.fingerprint != hex.EncodeToString(leafSum[:]) || bundle.caCertPEM == "" {
		t.Errorf("CA with a leaf: subject %q, fingerprint %q", bundle.subject, bundle.fingerprint)
	}
}

type adminMesh struct {
	settingsMesh
	rebootTo, resetTo uint32
	clockErr          error
	shutdown          int
	forgot            bool
}

func (m *adminMesh) AdminReboot(_ context.Context, node uint32, _ int) error {
	m.rebootTo = node
	return nil
}
func (m *adminMesh) AdminFactoryReset(_ context.Context, node uint32) error {
	m.resetTo = node
	return nil
}
func (m *adminMesh) MyNodeNum() uint32         { return 0x1234abcd }
func (m *adminMesh) AdminSetClock() error      { return m.clockErr }
func (m *adminMesh) AdminShutdown(d int) error { m.shutdown = d; return nil }
func (m *adminMesh) AdminForgetNodes() error   { m.forgot = true; return nil }

func TestNodeAdmin_Endpoints(t *testing.T) {
	mesh := &adminMesh{}
	s := &Server{mesh: mesh}
	serve(t, s, "POST", "/api/admin/reboot", `{"delay_secs":5}`)
	serve(t, s, "POST", "/api/admin/factory_reset", `{}`)
	if mesh.rebootTo != 0x1234abcd || mesh.resetTo != 0x1234abcd {
		t.Errorf("node_id 0 went to %x and %x, want the node this Bridge talks to", mesh.rebootTo, mesh.resetTo)
	}
	serve(t, s, "POST", "/api/admin/reboot", `{"node_id":7,"delay_secs":5}`)
	if mesh.rebootTo != 7 {
		t.Errorf("a named node went to %x", mesh.rebootTo)
	}
	if w := serve(t, s, "POST", "/api/admin/shutdown", `{"delay_secs":5}`); w.Code != http.StatusOK || mesh.shutdown != 5 {
		t.Errorf("shutdown: %d, delay %d", w.Code, mesh.shutdown)
	}
	if w := serve(t, s, "POST", "/api/admin/nodedb_reset", ``); w.Code != http.StatusOK || !mesh.forgot {
		t.Errorf("nodedb_reset: %d", w.Code)
	}
	if w := serve(t, s, "POST", "/api/admin/set_clock", ``); w.Code != http.StatusOK {
		t.Errorf("set_clock: %d", w.Code)
	}
	mesh.clockErr = transport.ErrClockUntrusted
	if w := serve(t, s, "POST", "/api/admin/set_clock", ``); w.Code != http.StatusConflict {
		t.Errorf("set_clock with an untrusted clock: %d", w.Code)
	}
	type plain struct{ transport.MeshTransport }
	if w := serve(t, &Server{mesh: plain{}}, "POST", "/api/admin/shutdown", `{}`); w.Code != http.StatusServiceUnavailable {
		t.Errorf("a transport without node admin: %d", w.Code)
	}
}

// The crossings are served newest first. [MESHSAT-1414]
func TestGeofenceEvents_Endpoint(t *testing.T) {
	g := engine.NewGeofenceMonitor()
	g.AddZone(engine.GeofenceZone{ID: "z", Name: "Home", AlertOn: "both", Polygon: []engine.LatLon{{Lat: 0, Lon: 0}, {Lat: 0, Lon: 1}, {Lat: 1, Lon: 1}, {Lat: 1, Lon: 0}}})
	g.CheckPosition("!a1b3c2ec", 0.5, 0.5)
	s := &Server{}
	s.SetGeofenceMonitor(g)
	w := serve(t, s, "GET", "/api/geofences/events", "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"zone_name":"Home"`) || !strings.Contains(w.Body.String(), `"event":"enter"`) {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	if w := serve(t, &Server{}, "GET", "/api/geofences/events", ""); w.Code != http.StatusServiceUnavailable {
		t.Errorf("without a monitor: %d", w.Code)
	}
}
