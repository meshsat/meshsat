package codec

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEncodeCanned(t *testing.T) {
	data := EncodeCanned(1)
	if len(data) != 2 {
		t.Fatalf("expected 2 bytes, got %d", len(data))
	}
	if data[0] != HeaderCanned {
		t.Errorf("header: got 0x%02X, want 0x%02X", data[0], HeaderCanned)
	}
	if data[1] != 1 {
		t.Errorf("id: got %d, want 1", data[1])
	}
}

func TestDecodeCanned(t *testing.T) {
	for id, want := range DefaultCodebook {
		data := EncodeCanned(id)
		got, err := DecodeCanned(data)
		if err != nil {
			t.Errorf("id %d: decode error: %v", id, err)
			continue
		}
		if got != want {
			t.Errorf("id %d: got %q, want %q", id, got, want)
		}
	}
}

func TestDecodeCannedUnknownID(t *testing.T) {
	data := EncodeCanned(255)
	_, err := DecodeCanned(data)
	if err == nil {
		t.Error("expected error for unknown message ID")
	}
}

func TestDecodeCannedShortData(t *testing.T) {
	_, err := DecodeCanned([]byte{HeaderCanned})
	if err == nil {
		t.Error("expected error for short data")
	}
}

func TestDecodeCannedWrongHeader(t *testing.T) {
	_, err := DecodeCanned([]byte{0xFF, 0x01})
	if err == nil {
		t.Error("expected error for wrong header")
	}
}

func TestIsCanned(t *testing.T) {
	if !IsCanned([]byte{HeaderCanned, 0x01}) {
		t.Error("should detect canned frame")
	}
	if IsCanned([]byte{0x50, 0x01}) {
		t.Error("should not detect position frame as canned")
	}
	if IsCanned(nil) {
		t.Error("should not detect nil as canned")
	}
	if IsCanned([]byte{}) {
		t.Error("should not detect empty as canned")
	}
}

func TestLookupByText(t *testing.T) {
	id, ok := LookupByText("Copy.")
	if !ok {
		t.Fatal("expected to find 'Copy.'")
	}
	if id != 1 {
		t.Errorf("got id %d, want 1", id)
	}

	_, ok = LookupByText("This message does not exist.")
	if ok {
		t.Error("expected not found for unknown text")
	}
}

func TestLookupByTextRoundTrip(t *testing.T) {
	for id, text := range DefaultCodebook {
		gotID, ok := LookupByText(text)
		if !ok {
			t.Errorf("reverse lookup failed for id %d (%q)", id, text)
			continue
		}
		if gotID != id {
			t.Errorf("reverse lookup: got id %d, want %d for %q", gotID, id, text)
		}
	}
}

func TestLoadCodebookFromFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "codebook.json")
	content := `{"1": "Alpha.", "2": "Bravo.", "3": "Charlie."}`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	cb, err := LoadCodebookFromFile(path)
	if err != nil {
		t.Fatalf("load error: %v", err)
	}

	msg, err := cb.Decode([]byte{HeaderCanned, 1})
	if err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if msg != "Alpha." {
		t.Errorf("got %q, want %q", msg, "Alpha.")
	}

	id, ok := cb.LookupByText("Bravo.")
	if !ok || id != 2 {
		t.Errorf("reverse lookup: got (%d, %v), want (2, true)", id, ok)
	}
}

func TestLoadCodebookFromFileMissing(t *testing.T) {
	_, err := LoadCodebookFromFile("/nonexistent/codebook.json")
	if err == nil {
		t.Error("expected error for missing file")
	}
}
