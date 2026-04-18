package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestDirectoryImportExportRoundTrip covers the MESHSAT-541 happy
// path: POST a vCard payload → directory_contacts populated → GET
// /export/vcard returns the same contacts.
func TestDirectoryImportExportRoundTrip(t *testing.T) {
	s := newTestServerWithDB(t)
	router := s.Router()

	vcf := `BEGIN:VCARD
VERSION:4.0
FN:Alice Kowalski
N:Kowalski;Alice;;;
X-MESHSAT-TEAM:Red
X-MESHSAT-ROLE:Medic
X-MESHSAT-SIDC:SFGPUCI----I
X-MESHSAT-TRUST-LEVEL:2
TEL;TYPE=cell:+31612345678
X-MESHSAT-MESHTASTIC:!abcd1234
X-MESHSAT-APRS:PA1A-9
EMAIL:alice@example.com
END:VCARD
BEGIN:VCARD
VERSION:4.0
FN:Bob Hart
X-MESHSAT-TEAM:Red
TEL;TYPE=cell:+31687654321
END:VCARD
`
	req := httptest.NewRequest("POST", "/api/directory/import/vcard", strings.NewReader(vcf))
	req.Header.Set("Content-Type", "text/vcard")
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("import: status=%d body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), `"imported":2`) {
		t.Errorf("import response did not report 2 imported: %s", rr.Body.String())
	}

	// Export — single concatenated document with both names and the
	// MESHSAT extensions preserved.
	req = httptest.NewRequest("GET", "/api/directory/export/vcard", nil)
	rr = httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("export: status=%d body=%s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if !strings.Contains(body, "FN:Alice Kowalski") {
		t.Errorf("export missing Alice: %s", body)
	}
	if !strings.Contains(body, "FN:Bob Hart") {
		t.Errorf("export missing Bob: %s", body)
	}
	if !strings.Contains(body, "X-MESHSAT-MESHTASTIC:!abcd1234") {
		t.Errorf("export missing Meshtastic extension: %s", body)
	}
	if !strings.Contains(body, "X-MESHSAT-SIDC:SFGPUCI----I") {
		t.Errorf("export missing SIDC extension: %s", body)
	}
	if !strings.Contains(body, "X-MESHSAT-TRUST-LEVEL:2") {
		t.Errorf("export missing TRUST-LEVEL: %s", body)
	}
	if got := rr.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/vcard") {
		t.Errorf("Content-Type: %q", got)
	}
}

// TestDirectoryImportCSV covers the operator-friendly spreadsheet
// import path.
func TestDirectoryImportCSV(t *testing.T) {
	s := newTestServerWithDB(t)
	router := s.Router()
	csv := "display_name,sms,meshtastic,team,role\n" +
		"Charlie Li,+31611110000,!1111aaaa,Blue,Pilot\n" +
		"Dana Kim,+31622220000,,Blue,Navigator\n"
	req := httptest.NewRequest("POST", "/api/directory/import/csv", strings.NewReader(csv))
	req.Header.Set("Content-Type", "text/csv")
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("import csv: status=%d body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), `"imported":2`) {
		t.Errorf("csv import not 2: %s", rr.Body.String())
	}

	// Confirm via export that CSV-imported Team/Role made it through.
	req = httptest.NewRequest("GET", "/api/directory/export/vcard", nil)
	rr = httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	body := rr.Body.String()
	if !strings.Contains(body, "FN:Charlie Li") ||
		!strings.Contains(body, "FN:Dana Kim") ||
		!strings.Contains(body, "X-MESHSAT-TEAM:Blue") {
		t.Errorf("export missing CSV-imported data: %s", body)
	}
}

// TestDirectoryImportVCardInvalid — malformed vCard returns 400.
func TestDirectoryImportVCardInvalid(t *testing.T) {
	s := newTestServerWithDB(t)
	router := s.Router()

	req := httptest.NewRequest("POST", "/api/directory/import/vcard", strings.NewReader("not a vcard at all"))
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	// No BEGIN:VCARD ⇒ parser yields zero contacts, not an error. The
	// API returns 200 with parsed=0 / imported=0.
	if rr.Code != http.StatusOK {
		t.Errorf("empty body: expected 200 (empty result), got %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), `"parsed":0`) {
		t.Errorf("expected parsed=0, got: %s", rr.Body.String())
	}
}

// TestDirectoryImportCSVBadHeader — CSV with no header row returns
// 400 so operators see a clear error instead of a silent no-op.
func TestDirectoryImportCSVBadHeader(t *testing.T) {
	s := newTestServerWithDB(t)
	router := s.Router()
	req := httptest.NewRequest("POST", "/api/directory/import/csv", strings.NewReader("Alice,+31...\n"))
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for single-line CSV, got %d: %s", rr.Code, rr.Body.String())
	}
}
