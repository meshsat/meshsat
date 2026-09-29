package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A link's transforms set on their own keep everything else about the link;
// a key that cannot encrypt is refused when saved. [MESHSAT-1412]
func TestSetInterfaceTransforms_KeepsTheRestOfTheLink(t *testing.T) {
	s, _ := switchServer(t)
	if _, err := s.db.Exec(`UPDATE interfaces SET config = '{"keep":"me"}', ingress_transforms = '[{"type":"base64"}]' WHERE id = 'iridium_0'`); err != nil {
		t.Fatal(err)
	}
	key := strings.Repeat("ab", 32)
	body := `{"egress_transforms":"[{\"type\":\"encrypt\",\"params\":{\"key\":\"` + key + `\"}},{\"type\":\"base64\"}]"}`
	req := httptest.NewRequest("PUT", "/api/interfaces/iridium_0/transforms", strings.NewReader(body))
	w := httptest.NewRecorder()
	s.Router().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("set: %d %s", w.Code, w.Body.String())
	}
	iface, err := s.db.GetInterface("iridium_0")
	if err != nil {
		t.Fatal(err)
	}
	if iface.Config != `{"keep":"me"}` || !iface.Enabled || iface.IngressTransforms != `[{"type":"base64"}]` || !strings.Contains(iface.EgressTransforms, key) {
		t.Fatalf("the link after the write: %+v", iface)
	}

	for _, bad := range []string{
		`{"egress_transforms":"[{\"type\":\"encrypt\",\"params\":{\"key\":\"0123\"}}]"}`,
		`{"ingress_transforms":"[{\"type\":\"rot13\"}]"}`,
		`{}`,
	} {
		w := httptest.NewRecorder()
		s.Router().ServeHTTP(w, httptest.NewRequest("PUT", "/api/interfaces/iridium_0/transforms", strings.NewReader(bad)))
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %s", bad, w.Code, w.Body.String())
		}
	}
	after, _ := s.db.GetInterface("iridium_0")
	if after.EgressTransforms != iface.EgressTransforms || after.IngressTransforms != iface.IngressTransforms {
		t.Fatalf("a refused write changed the link: %+v", after)
	}
	w = httptest.NewRecorder()
	s.Router().ServeHTTP(w, httptest.NewRequest("PUT", "/api/interfaces/nope_0/transforms", strings.NewReader(`{"egress_transforms":"[]"}`)))
	if w.Code != http.StatusNotFound {
		t.Errorf("unknown link: %d", w.Code)
	}
}

func TestTransformCapabilities(t *testing.T) {
	s, _ := switchServer(t)
	w := httptest.NewRecorder()
	s.Router().ServeHTTP(w, httptest.NewRequest("GET", "/api/transforms/capabilities", nil))
	var caps map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &caps); err != nil || w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	if caps["msvqsc_encode"] != false {
		t.Errorf("no encoder here, yet msvqsc_encode = %v", caps["msvqsc_encode"])
	}
}
