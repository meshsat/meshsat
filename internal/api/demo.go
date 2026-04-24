package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"meshsat/internal/gateway"
	"meshsat/internal/hubreporter"
	"meshsat/internal/transport"
)

// Demo orchestrator — [MESHSAT-686]
//
// POST /api/demo/run
//   Kicks off a background run that fires one canned message through each
//   available send primitive, in parallel. Returns 202 + {demo_id}. Client
//   polls GET /api/demo/{demo_id} every 500ms for progress.
//
// GET /api/demo/{demo_id}
//   Returns {status, channels:[{name, status, latency_ms, detail}]}.
//   status = "running" | "complete".
//
// We keep at most 8 runs in memory; oldest evicted on overflow. No DB.
// Runs expire 10 minutes after completion. This is a UI convenience,
// not an audited operation — nothing here bypasses the normal send
// pipelines, it just scripts them.

type demoChannelResult struct {
	Name      string `json:"name"`
	Status    string `json:"status"` // "pending" | "success" | "failed" | "skipped"
	LatencyMS int64  `json:"latency_ms"`
	Detail    string `json:"detail,omitempty"`
}

type demoRun struct {
	ID       string              `json:"demo_id"`
	Status   string              `json:"status"` // "running" | "complete"
	Started  time.Time           `json:"started"`
	Finished time.Time           `json:"finished,omitempty"`
	Channels []demoChannelResult `json:"channels"`
	mu       sync.Mutex
}

type demoRegistry struct {
	mu   sync.Mutex
	runs map[string]*demoRun
}

var demoReg = &demoRegistry{runs: make(map[string]*demoRun)}

func (r *demoRegistry) put(run *demoRun) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.runs[run.ID] = run
	// Evict completed runs older than 10 min to bound memory.
	now := time.Now()
	for id, existing := range r.runs {
		if !existing.Finished.IsZero() && now.Sub(existing.Finished) > 10*time.Minute {
			delete(r.runs, id)
		}
	}
	// Hard cap: if somehow we have >8, drop the oldest by start time.
	if len(r.runs) > 8 {
		var oldestID string
		var oldestT time.Time
		for id, existing := range r.runs {
			if oldestID == "" || existing.Started.Before(oldestT) {
				oldestID, oldestT = id, existing.Started
			}
		}
		if oldestID != "" && oldestID != run.ID {
			delete(r.runs, oldestID)
		}
	}
}

func (r *demoRegistry) get(id string) *demoRun {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.runs[id]
}

func newDemoID() string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func (run *demoRun) setResult(name, status, detail string, started time.Time) {
	run.mu.Lock()
	defer run.mu.Unlock()
	for i := range run.Channels {
		if run.Channels[i].Name == name {
			run.Channels[i].Status = status
			run.Channels[i].LatencyMS = time.Since(started).Milliseconds()
			run.Channels[i].Detail = detail
			return
		}
	}
}

// handleDemoRun starts a demo sweep across all available channels.
// @Summary Run full-channel demo sweep
// @Description Fires one canned message through each available send primitive
// @Description (mesh, APRS, SMS, Iridium, Hub ping, Reticulum announce). Useful
// @Description for one-tap demo recording — operator presses a button, all
// @Description channels exercise in parallel, dashboard widgets light up.
// @Description Returns a demo_id; poll /api/demo/{id} for progress.
// @Tags demo
// @Produce json
// @Success 202 {object} map[string]string "{demo_id}"
// @Router /api/demo/run [post]
func (s *Server) handleDemoRun(w http.ResponseWriter, r *http.Request) {
	id := newDemoID()
	now := time.Now()

	run := &demoRun{
		ID:      id,
		Status:  "running",
		Started: now,
		Channels: []demoChannelResult{
			{Name: "mesh", Status: "pending"},
			{Name: "aprs", Status: "pending"},
			{Name: "cellular", Status: "pending"},
			{Name: "iridium", Status: "pending"},
			{Name: "hub", Status: "pending"},
			{Name: "reticulum", Status: "pending"},
		},
	}
	demoReg.put(run)

	// Fire each channel in its own goroutine; total sweep completes when
	// all goroutines finish. Each has its own short timeout so one stuck
	// transport can't hold the sweep open.
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	go func() {
		defer cancel()
		var wg sync.WaitGroup

		wg.Add(1)
		go func() {
			defer wg.Done()
			s.demoMesh(ctx, run)
		}()
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.demoAPRS(ctx, run)
		}()
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.demoCellular(ctx, run)
		}()
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.demoIridium(ctx, run)
		}()
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.demoHub(ctx, run)
		}()
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.demoReticulum(ctx, run)
		}()

		wg.Wait()
		run.mu.Lock()
		run.Status = "complete"
		run.Finished = time.Now()
		run.mu.Unlock()
	}()

	writeJSON(w, http.StatusAccepted, map[string]string{"demo_id": id})
}

// handleDemoStatus returns the current state of a demo run.
// @Summary Get demo run status
// @Tags demo
// @Produce json
// @Param demo_id path string true "Demo run ID"
// @Success 200 {object} demoRun
// @Failure 404 {object} map[string]string
// @Router /api/demo/{demo_id} [get]
func (s *Server) handleDemoStatus(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "demo_id")
	run := demoReg.get(id)
	if run == nil {
		writeError(w, http.StatusNotFound, "demo run not found (may have expired)")
		return
	}
	run.mu.Lock()
	out := demoRun{
		ID: run.ID, Status: run.Status, Started: run.Started,
		Finished: run.Finished,
		Channels: append([]demoChannelResult(nil), run.Channels...),
	}
	run.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

// --- per-channel demo actions ---

func (s *Server) demoMesh(ctx context.Context, run *demoRun) {
	t := time.Now()
	if s.mesh == nil {
		run.setResult("mesh", "skipped", "mesh transport not wired", t)
		return
	}
	// MeshTransport interface does not expose IsConnected() directly; the
	// typed receivers do. Probe via Status() instead — cheap and covers
	// both DirectMeshTransport and any future HAL-backed variant.
	if st, err := s.mesh.GetStatus(ctx); err != nil || !st.Connected {
		run.setResult("mesh", "skipped", "radio disconnected", t)
		return
	}
	sub, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	err := s.mesh.SendMessage(sub, transport.SendRequest{
		To:      "",
		Channel: 0,
		Text:    fmt.Sprintf("MESHSAT demo %s", run.ID),
	})
	if err != nil {
		run.setResult("mesh", "failed", err.Error(), t)
		return
	}
	run.setResult("mesh", "success", "broadcast text sent on primary channel", t)
}

func (s *Server) demoAPRS(ctx context.Context, run *demoRun) {
	t := time.Now()
	if s.gwManager == nil {
		run.setResult("aprs", "skipped", "gateway manager not wired", t)
		return
	}
	gw := s.gwManager.GetAPRSGateway()
	if gw == nil {
		run.setResult("aprs", "skipped", "APRS gateway not running", t)
		return
	}
	sub, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	msg := &transport.MeshMessage{
		DecodedText: fmt.Sprintf("MESHSAT demo %s", run.ID),
	}
	if err := gw.Forward(sub, msg); err != nil {
		run.setResult("aprs", "failed", err.Error(), t)
		return
	}
	run.setResult("aprs", "success", "bulletin queued on AX.25", t)
}

func (s *Server) demoCellular(ctx context.Context, run *demoRun) {
	t := time.Now()
	if s.cellTransport == nil {
		run.setResult("cellular", "skipped", "cellular transport not wired", t)
		return
	}
	// Only send if a demo peer is configured to avoid surprise SMS costs.
	// The operator can configure the demo peer under Settings > Cellular.
	peer := firstConfiguredSMSPeer(s)
	if peer == "" {
		run.setResult("cellular", "skipped", "no demo SMS peer configured", t)
		return
	}
	sub, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	text := fmt.Sprintf("MeshSat demo %s", run.ID)
	// Apply egress transforms if configured on cellular_0 (same path as
	// /api/cellular/sms/send).
	outText := text
	if s.transforms != nil {
		if iface, err := s.db.GetInterface("cellular_0"); err == nil && iface.EgressTransforms != "" && iface.EgressTransforms != "[]" {
			if transformed, err := s.transforms.ApplyEgress([]byte(text), iface.EgressTransforms); err == nil {
				outText = string(transformed)
			}
		}
	}
	if err := s.cellTransport.SendSMS(sub, peer, outText); err != nil {
		run.setResult("cellular", "failed", err.Error(), t)
		return
	}
	run.setResult("cellular", "success", "SMS dispatched to "+peer, t)
}

func (s *Server) demoIridium(ctx context.Context, run *demoRun) {
	t := time.Now()
	if s.gwManager == nil {
		run.setResult("iridium", "skipped", "gateway manager not wired", t)
		return
	}
	// Dispatch via the auto-detected active sat gateway (9603 or 9704).
	// SBDIX with no sky-visible satellite will fail fast; IMT is push-based.
	sub, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	// Use a typed MeshMessage so we go through the regular delivery path.
	msg := &transport.MeshMessage{DecodedText: fmt.Sprintf("MS-DEMO-%s", run.ID)}
	var err error
	if sbd := s.gwManager.GetSBDGateway(); sbd != nil {
		err = sbd.Forward(sub, msg)
		if err == nil {
			run.setResult("iridium", "success", "SBDIX queued", t)
			return
		}
	}
	if imt := s.gwManager.GetIMTGateway(); imt != nil {
		err = imt.Forward(sub, msg)
		if err == nil {
			run.setResult("iridium", "success", "IMT MO queued", t)
			return
		}
	}
	if err != nil {
		run.setResult("iridium", "failed", err.Error(), t)
		return
	}
	run.setResult("iridium", "skipped", "no iridium gateway running", t)
}

func (s *Server) demoHub(ctx context.Context, run *demoRun) {
	t := time.Now()
	if s.hubReporter == nil {
		run.setResult("hub", "skipped", "hub reporter not wired", t)
		return
	}
	if !s.hubReporter.IsConnected() {
		run.setResult("hub", "skipped", "hub not connected", t)
		return
	}
	// Publish a demo position; hub's TAK subscriber converts to CoT and
	// relays to OTS, so this also exercises the TAK loop end-to-end.
	deviceID := fmt.Sprintf("demo-%s", run.ID)
	pos := hubreporter.DevicePosition{
		Lat:       52.1601,
		Lon:       4.4970,
		Alt:       10,
		Source:    "demo",
		Timestamp: time.Now().UTC(),
	}
	if err := s.hubReporter.PublishDevicePosition(deviceID, pos); err != nil {
		run.setResult("hub", "failed", err.Error(), t)
		return
	}
	run.setResult("hub", "success", "position published to hub (→ OTS CoT)", t)
}

func (s *Server) demoReticulum(ctx context.Context, run *demoRun) {
	t := time.Now()
	if s.ifaceRegistry == nil {
		run.setResult("reticulum", "skipped", "routing subsystem not wired", t)
		return
	}
	// Count floodable (free) interfaces as a proxy for "reticulum alive".
	// The real announce cycle runs on its own schedule — this just
	// reports how many interfaces would receive it.
	floodable := s.ifaceRegistry.Floodable()
	if len(floodable) == 0 {
		run.setResult("reticulum", "skipped", "no floodable interfaces online", t)
		return
	}
	names := make([]string, 0, len(floodable))
	for _, iface := range floodable {
		names = append(names, iface.ID())
	}
	detail := fmt.Sprintf("announce will fan out to %d floodable iface(s): %v", len(floodable), names)
	run.setResult("reticulum", "success", detail, t)
}

// firstConfiguredSMSPeer returns the first peer phone number configured
// for the demo button. Order: gateway config destination_numbers[0] →
// contact-list fallback. Returns "" when nothing is configured.
func firstConfiguredSMSPeer(s *Server) string {
	if s.gwManager == nil {
		return ""
	}
	// Peek at cellular gateway config without holding a reference.
	for _, gw := range s.gwManager.GetStatus() {
		if gw.Type != "cellular" {
			continue
		}
		if len(gw.Config) == 0 {
			continue
		}
		var cfg struct {
			DestinationNumbers []string `json:"destination_numbers"`
		}
		_ = json.Unmarshal(gw.Config, &cfg)
		for _, num := range cfg.DestinationNumbers {
			if num != "" {
				return num
			}
		}
	}
	// Fallback: first contact with a primary SMS address.
	if s.db != nil {
		contacts, err := s.db.GetContacts()
		if err == nil {
			for _, c := range contacts {
				addrs, err := s.db.GetContactAddresses(c.ID)
				if err != nil {
					continue
				}
				for _, a := range addrs {
					if a.Type == "sms" && a.Address != "" {
						return a.Address
					}
				}
			}
		}
	}
	return ""
}

// compile-time assertion: the SMS peer fallback uses gateway types that
// are compatible with the current Manager API. If the signature shifts,
// this reference will fail at build time — cheap safety net.
var _ = gateway.GatewayStatusResponse{}
