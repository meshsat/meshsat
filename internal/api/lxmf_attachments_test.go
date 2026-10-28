package api

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"meshsat/internal/channel"
	"meshsat/internal/database"
	"meshsat/internal/engine"
	"meshsat/internal/lxmf"
	"meshsat/internal/reticulum"
	"meshsat/internal/rns"
	"meshsat/internal/rnsstack"
)

type noTx struct{}

func (noTx) Transmit(string, []byte) error { return nil }
func (noTx) Floodable() []string           { return nil }
func (noTx) HWMTU(string) int              { return 500 }
func (noTx) Bitrate(string) int            { return 0 }

// lxmfTestServer is a server with a database, a dispatcher and an LXMF
// router on a node that transmits nowhere: enough for the handlers.
func lxmfTestServer(t *testing.T) *Server {
	t.Helper()
	s := newTestServerWithDB(t)
	d := engine.NewDispatcher(s.db, channel.NewRegistry(), nil, nil)
	s.SetDispatcher(d)
	id, _ := reticulum.GenerateIdentity()
	node, err := rns.New(rns.Config{Identity: id}, noTx{})
	if err != nil {
		t.Fatal(err)
	}
	s.SetLXMFRouter(lxmf.New(node, lxmf.Config{Identity: id, DisplayName: "test"}))
	return s
}

func b64(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

// The send endpoint takes a voice note and a photo into the ledger row's
// body, refuses what the message cannot carry, and lets a note go without
// text. [MESHSAT-1735]
func TestLXMFSendWithAttachments(t *testing.T) {
	s := lxmfTestServer(t)
	router := s.Router()
	to := strings.Repeat("ab", 16)
	post := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/api/lxmf/send", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w
	}
	frames := b64(bytes.Repeat([]byte{1}, 300))
	w := post(`{"to":"` + to + `","audio":{"mode":3,"data_b64":"` + frames + `"},"image":{"format":"webp","data_b64":"` + b64([]byte("RIFF....WEBP")) + `"}}`)
	if w.Code != http.StatusAccepted {
		t.Fatalf("send: %d %s", w.Code, w.Body.String())
	}
	var resp lxmfSendResponse
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	del, err := s.db.GetDelivery(resp.DeliveryID)
	if err != nil || del.Channel != "lxmf_0" || del.Class != database.DeliveryClassLXMF || del.Destination != to {
		t.Fatalf("row %+v %v", del, err)
	}
	if del.TextPreview != "[image webp 12 B, audio Codec2 700C 300 B]" {
		t.Fatalf("preview %q", del.TextPreview)
	}
	var body rnsstack.LXMFBody
	if err := json.Unmarshal(del.Payload, &body); err != nil || body.Audio == nil || body.Audio.Mode != 3 || body.Audio.DataB64 != frames || body.Image == nil || body.Image.Format != "webp" {
		t.Fatalf("body %+v %v", body, err)
	}
	atts, _ := body.Attachments()
	if len(atts) != 2 || len(atts[1].Data) != 300 {
		t.Fatalf("attachments %+v", atts)
	}

	for name, tc := range map[string]struct {
		body string
		code int
	}{
		"too large":   {`{"to":"` + to + `","content":"x","image":{"format":"webp","data_b64":"` + b64(make([]byte, lxmf.MaxAttachmentBytes+1)) + `"}}`, http.StatusRequestEntityTooLarge},
		"bad mode":    {`{"to":"` + to + `","content":"x","audio":{"mode":12,"data_b64":"` + frames + `"}}`, http.StatusBadRequest},
		"bad base64":  {`{"to":"` + to + `","content":"x","audio":{"mode":3,"data_b64":"***"}}`, http.StatusBadRequest},
		"bad format":  {`{"to":"` + to + `","content":"x","image":{"format":"../x","data_b64":"` + frames + `"}}`, http.StatusBadRequest},
		"empty audio": {`{"to":"` + to + `","content":"x","audio":{"mode":3,"data_b64":""}}`, http.StatusBadRequest},
		"nothing":     {`{"to":"` + to + `"}`, http.StatusBadRequest},
	} {
		if w := post(tc.body); w.Code != tc.code {
			t.Fatalf("%s: %d %s", name, w.Code, w.Body.String())
		}
	}
	// Text alone still goes as before, with a plain body.
	w = post(`{"to":"` + to + `","content":"hello"}`)
	if w.Code != http.StatusAccepted {
		t.Fatalf("text: %d %s", w.Code, w.Body.String())
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	del, _ = s.db.GetDelivery(resp.DeliveryID)
	if del.TextPreview != "hello" || strings.Contains(string(del.Payload), "audio") {
		t.Fatalf("text row %+v", del)
	}
}

// A filed message's attachments are listed with their media types and
// served with the right headers, by row id or LXMF hash. [MESHSAT-1735]
func TestLXMFAttachmentsRoutes(t *testing.T) {
	s := newTestServerWithDB(t)
	router := s.Router()
	id, err := s.db.InsertMessageWithStatus(&database.Message{FromNode: "lxmf:aa", ToNode: "lxmf:bb", PortNum: 1, PortNumName: "LXMF", DecodedText: "note", Direction: "rx", Transport: "reticulum"}, "received")
	if err != nil {
		t.Fatal(err)
	}
	hash := strings.Repeat("cd", 32)
	frames := bytes.Repeat([]byte{2}, 50)
	if err := s.db.InsertLXMFAttachments(id, hash, []database.LXMFAttachment{{Kind: "image", Format: "webp"}, {Kind: "audio", Mode: 3}}, [][]byte{[]byte("RIFF"), frames}); err != nil {
		t.Fatal(err)
	}
	get := func(path string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		return w
	}
	for _, key := range []string{"1", hash} {
		w := get("/api/lxmf/messages/" + key + "/attachments")
		if w.Code != http.StatusOK {
			t.Fatalf("list by %s: %d %s", key, w.Code, w.Body.String())
		}
		var resp lxmfAttachmentsResponse
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil || resp.MessageID != id || resp.LXMFHash != hash || len(resp.Attachments) != 2 {
			t.Fatalf("list by %s: %+v %v", key, resp, err)
		}
		if a := resp.Attachments[1]; a.Kind != "audio" || a.Mode != 3 || a.ContentType != "audio/codec2" || a.URL != "/api/lxmf/messages/1/attachments/1" || a.Size != 50 {
			t.Fatalf("audio item %+v", a)
		}
		if a := resp.Attachments[0]; a.ContentType != "image/webp" || a.Format != "webp" {
			t.Fatalf("image item %+v", a)
		}
	}
	w := get("/api/lxmf/messages/1/attachments/1")
	if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "audio/codec2" || w.Header().Get("X-LXMF-Audio-Mode") != "3" ||
		w.Header().Get("Content-Length") != "50" || !bytes.Equal(w.Body.Bytes(), frames) {
		t.Fatalf("audio bytes: %d %v %d", w.Code, w.Header(), w.Body.Len())
	}
	w = get("/api/lxmf/messages/" + hash + "/attachments/0")
	if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "image/webp" || w.Header().Get("X-LXMF-Audio-Mode") != "" || w.Body.String() != "RIFF" {
		t.Fatalf("image bytes: %d %v", w.Code, w.Header())
	}
	for _, path := range []string{"/api/lxmf/messages/1/attachments/2", "/api/lxmf/messages/1/attachments/-1", "/api/lxmf/messages/9/attachments/0",
		"/api/lxmf/messages/" + strings.Repeat("ef", 32) + "/attachments/0", "/api/lxmf/messages/zz/attachments"} {
		if w := get(path); w.Code != http.StatusNotFound {
			t.Fatalf("%s: %d", path, w.Code)
		}
	}
	// A message without attachments lists an empty array, not an error.
	w = get("/api/lxmf/messages/9/attachments")
	if w.Code != http.StatusNotFound {
		t.Fatalf("unknown message list: %d", w.Code)
	}
	plain, _ := s.db.InsertMessageWithStatus(&database.Message{FromNode: "lxmf:aa", ToNode: "lxmf:bb", PortNum: 1, PortNumName: "LXMF", DecodedText: "text", Direction: "rx", Transport: "reticulum"}, "received")
	w = get("/api/lxmf/messages/" + strconv.FormatInt(plain, 10) + "/attachments")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"attachments":[]`) {
		t.Fatalf("plain message: %d %s", w.Code, w.Body.String())
	}
}
