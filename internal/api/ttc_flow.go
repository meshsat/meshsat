package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/rs/zerolog/log"

	"meshsat/internal/database"
)

// Booth flow selector — [MESHSAT-962]
//
// The TTC screen lets a visitor pick how the next message typed on this
// kit's handheld leaves the kit. Three paths, one enabled at a time:
//
//	aprs     mesh_0 -> peer_link (APRS first, SMS to the peer kit when the
//	         receiver is deaf), the direct relay of MESHSAT-857
//	b2b_sms  mesh_0 -> cellular_0, SMS straight to the peer kit's SIM
//	hub_sms  mesh_0 -> cellular_0, SMS to the Hub's number; the Hub's own
//	         route forwards it to the peer kit's SIM
//
// The selection is per kit and governs EGRESS only (owner ruling 8 Sep
// 2026). Inbound stays open on both kits for all three sources, so the
// reply follows whatever the other panel has selected and nothing has to
// be synchronised between the kits (there is no network between them at
// the booth). The far screen tells the lanes apart by the SMS sender: the
// peer's SIM means b2b_sms, the Hub's number means hub_sms.
//
// The three egress rules are ordinary access rules named "ttc:<path>", so
// they show up in Settings > Rules like any other and nothing here bypasses
// the rules engine or the delivery ledger. Per-rule SMS destinations ride
// on forward_options.sms_contacts (resolved by the DeliveryWorker, they
// REPLACE the gateway's destination_numbers), so the two SMS paths only
// differ by the contact they point at.
//
// GET  /api/ttc/flow         current path, the three rules, readiness
// PUT  /api/ttc/flow         {"path":"aprs"|"b2b_sms"|"hub_sms"}
// POST /api/ttc/flow/setup   {"peer_number","hub_number"} one-time prep:
//                            contacts, the three egress rules, the two
//                            inbound rules, allowed_senders on cellular_0

const (
	ttcFlowKey        = "ttc_flow"
	ttcPeerNumberKey  = "ttc_peer_number"
	ttcHubNumberKey   = "ttc_hub_number"
	ttcRulePrefix     = "ttc:"
	ttcPeerGroupID    = "peer_link"
	ttcTextOnlyFilter = `{"portnums":"[1]"}`
	ttcRateLimit      = 6
)

var ttcPaths = []string{"aprs", "b2b_sms", "hub_sms"}

func ttcPathValid(p string) bool {
	for _, x := range ttcPaths {
		if x == p {
			return true
		}
	}
	return false
}

// ttcFlowRule is the per-path rule view returned to the screen.
type ttcFlowRule struct {
	ID        int64  `json:"id"`
	Enabled   bool   `json:"enabled"`
	ForwardTo string `json:"forward_to"`
	Contact   string `json:"contact,omitempty"` // phone number for the SMS paths
}

// ttcFlowStatus is the GET/PUT/setup response.
type ttcFlowStatus struct {
	Path       string                 `json:"path"`
	Available  []string               `json:"available"`
	Rules      map[string]ttcFlowRule `json:"rules"`
	PeerNumber string                 `json:"peer_number,omitempty"`
	HubNumber  string                 `json:"hub_number,omitempty"`
	Ready      bool                   `json:"ready"`
	Issues     []string               `json:"issues,omitempty"`
}

// ttcNumbers returns the peer and Hub numbers: system_config first, then
// the cellular gateway's first destination (peer) and the env (Hub).
func (s *Server) ttcNumbers() (peer, hub string) {
	if v, err := s.db.GetSystemConfig(ttcPeerNumberKey); err == nil {
		peer = strings.TrimSpace(v)
	}
	if v, err := s.db.GetSystemConfig(ttcHubNumberKey); err == nil {
		hub = strings.TrimSpace(v)
	}
	if peer == "" {
		if gc, err := s.db.GetGatewayConfig("cellular"); err == nil && gc != nil {
			var cfg struct {
				DestinationNumbers []string `json:"destination_numbers"`
			}
			if json.Unmarshal([]byte(gc.Config), &cfg) == nil && len(cfg.DestinationNumbers) > 0 {
				peer = strings.TrimSpace(cfg.DestinationNumbers[0])
			}
		}
	}
	if hub == "" {
		hub = strings.TrimSpace(os.Getenv("MESHSAT_HUB_SMS_NUMBER"))
	}
	return peer, hub
}

// ttcContactID finds or creates the sms_contacts row for a number.
func (s *Server) ttcContactID(name, phone string) (int64, error) {
	contacts, err := s.db.GetSMSContacts()
	if err != nil {
		return 0, err
	}
	for _, c := range contacts {
		if c.Phone == phone {
			return c.ID, nil
		}
	}
	return s.db.CreateSMSContact(name, phone, "created by the TTC flow selector [MESHSAT-962]", false)
}

// ttcContactPhone resolves a rule's forward_options.sms_contacts[0] to a
// phone number for the status view.
func (s *Server) ttcContactPhone(r *database.AccessRule) string {
	if r == nil || r.ForwardOptions == "" || r.ForwardOptions == "{}" {
		return ""
	}
	var opts struct {
		SMSContacts []int64 `json:"sms_contacts"`
	}
	if json.Unmarshal([]byte(r.ForwardOptions), &opts) != nil || len(opts.SMSContacts) == 0 {
		return ""
	}
	contacts, err := s.db.GetSMSContacts()
	if err != nil {
		return ""
	}
	for _, c := range contacts {
		if c.ID == opts.SMSContacts[0] {
			return c.Phone
		}
	}
	return ""
}

// ttcRules returns the "ttc:<path>" egress rules keyed by path.
func (s *Server) ttcRules() (map[string]*database.AccessRule, []database.AccessRule, error) {
	all, err := s.db.GetAllAccessRules()
	if err != nil {
		return nil, nil, err
	}
	out := map[string]*database.AccessRule{}
	for i := range all {
		r := &all[i]
		if r.InterfaceID != "mesh_0" || r.Direction != "egress" {
			continue
		}
		if strings.HasPrefix(r.Name, ttcRulePrefix) {
			p := strings.TrimPrefix(r.Name, ttcRulePrefix)
			if ttcPathValid(p) {
				out[p] = r
			}
		}
	}
	return out, all, nil
}

// ttcEnsureRules creates the three egress rules that are missing. An
// existing mesh_0 -> peer_link rule that is not ttc-named (rule 1 of
// MESHSAT-857 on both kits) is adopted as "ttc:aprs" so its ID, filters
// and match counters survive. Returns the rules keyed by path.
func (s *Server) ttcEnsureRules(peer, hub string) (map[string]*database.AccessRule, []string, error) {
	var issues []string
	rules, all, err := s.ttcRules()
	if err != nil {
		return nil, nil, err
	}
	// Filters and rate limit follow the adopted rule when there is one.
	filters := ttcTextOnlyFilter
	if rules["aprs"] == nil {
		for i := range all {
			r := &all[i]
			if r.InterfaceID == "mesh_0" && r.Direction == "egress" && r.Action == "forward" &&
				r.ForwardTo == ttcPeerGroupID && !strings.HasPrefix(r.Name, ttcRulePrefix) {
				r.Name = ttcRulePrefix + "aprs"
				if err := s.db.UpdateAccessRule(r); err != nil {
					return nil, nil, fmt.Errorf("adopt rule %d: %w", r.ID, err)
				}
				rules["aprs"] = r
				log.Info().Int64("rule", r.ID).Msg("ttc flow: adopted the peer_link relay rule as ttc:aprs")
				break
			}
		}
	}
	if r := rules["aprs"]; r != nil && r.Filters != "" && r.Filters != "{}" {
		filters = r.Filters
	}
	if rules["aprs"] == nil {
		fwd := ttcPeerGroupID
		if g, err := s.db.GetFailoverGroup(ttcPeerGroupID); err != nil || g == nil {
			fwd = "aprs_0"
			issues = append(issues, "failover group peer_link is missing; the aprs path forwards to aprs_0 without the SMS fallback")
		}
		r := &database.AccessRule{InterfaceID: "mesh_0", Direction: "egress", Priority: 1, Name: ttcRulePrefix + "aprs",
			Enabled: false, Action: "forward", ForwardTo: fwd, Filters: filters, ForwardOptions: "{}",
			RateLimitPerMin: ttcRateLimit, RateLimitWindow: 60}
		id, err := s.db.InsertAccessRule(r)
		if err != nil {
			return nil, nil, err
		}
		r.ID = id
		rules["aprs"] = r
	}
	for _, sp := range []struct{ path, label, number string }{{"b2b_sms", "peer kit", peer}, {"hub_sms", "MeshSat Hub", hub}} {
		if sp.number == "" {
			issues = append(issues, fmt.Sprintf("%s: no number known (setup with %s_number, or set the cellular destination)", sp.path, map[string]string{"b2b_sms": "peer", "hub_sms": "hub"}[sp.path]))
			continue
		}
		cid, err := s.ttcContactID(sp.label, sp.number)
		if err != nil {
			return nil, nil, fmt.Errorf("%s contact: %w", sp.path, err)
		}
		opts := fmt.Sprintf(`{"sms_contacts":[%d]}`, cid)
		if r := rules[sp.path]; r != nil {
			if r.ForwardOptions != opts || r.ForwardTo != "cellular_0" {
				r.ForwardOptions = opts
				r.ForwardTo = "cellular_0"
				if err := s.db.UpdateAccessRule(r); err != nil {
					return nil, nil, err
				}
			}
			continue
		}
		r := &database.AccessRule{InterfaceID: "mesh_0", Direction: "egress", Priority: 1, Name: ttcRulePrefix + sp.path,
			Enabled: false, Action: "forward", ForwardTo: "cellular_0", Filters: filters, ForwardOptions: opts,
			RateLimitPerMin: ttcRateLimit, RateLimitWindow: 60}
		id, err := s.db.InsertAccessRule(r)
		if err != nil {
			return nil, nil, err
		}
		r.ID = id
		rules[sp.path] = r
	}
	return rules, issues, nil
}

// ttcEnsureInbound makes sure aprs_0 -> mesh_0 and cellular_0 -> mesh_0
// ingress rules exist and are enabled. They are never created with a
// sender filter: the cellular gateway's allowed_senders already gates who
// may reach the mesh over SMS.
func (s *Server) ttcEnsureInbound() error {
	all, err := s.db.GetAllAccessRules()
	if err != nil {
		return err
	}
	for _, src := range []string{"aprs_0", "cellular_0"} {
		found := false
		for i := range all {
			r := &all[i]
			if r.InterfaceID == src && r.Direction == "ingress" && r.Action == "forward" && r.ForwardTo == "mesh_0" {
				found = true
				if !r.Enabled {
					if err := s.db.SetAccessRuleEnabled(r.ID, true); err != nil {
						return err
					}
				}
			}
		}
		if !found {
			r := &database.AccessRule{InterfaceID: src, Direction: "ingress", Priority: 10, Name: ttcRulePrefix + "in " + src,
				Enabled: true, Action: "forward", ForwardTo: "mesh_0", Filters: "{}", ForwardOptions: "{}",
				RateLimitPerMin: ttcRateLimit, RateLimitWindow: 60}
			if _, err := s.db.InsertAccessRule(r); err != nil {
				return err
			}
		}
	}
	return nil
}

// ttcEnsureAllowedSenders adds the peer and Hub numbers to the cellular
// gateway's allowed_senders. Reconfiguring restarts the gateway (the modem
// answers again about 20 s later), so it only happens when a number is
// actually missing. Returns true when the gateway was reconfigured.
func (s *Server) ttcEnsureAllowedSenders(ctx context.Context, peer, hub string) (bool, error) {
	gc, err := s.db.GetGatewayConfig("cellular")
	if err != nil || gc == nil {
		return false, fmt.Errorf("no cellular gateway configured")
	}
	var cfg map[string]any
	if err := json.Unmarshal([]byte(gc.Config), &cfg); err != nil {
		return false, fmt.Errorf("cellular config: %w", err)
	}
	var have []string
	if raw, ok := cfg["allowed_senders"].([]any); ok {
		for _, v := range raw {
			if str, ok := v.(string); ok {
				have = append(have, str)
			}
		}
	}
	if len(have) == 0 {
		// Empty means "anyone"; leave that alone.
		return false, nil
	}
	changed := false
	for _, n := range []string{peer, hub} {
		if n == "" {
			continue
		}
		present := false
		for _, h := range have {
			if h == n {
				present = true
				break
			}
		}
		if !present {
			have = append(have, n)
			changed = true
		}
	}
	if !changed {
		return false, nil
	}
	cfg["allowed_senders"] = have
	b, err := json.Marshal(cfg)
	if err != nil {
		return false, err
	}
	if s.gwManager == nil {
		return false, fmt.Errorf("gateway manager not available")
	}
	if err := s.gwManager.ConfigureInstance(ctx, "cellular", gc.InstanceID, gc.Enabled, string(b)); err != nil {
		return false, err
	}
	log.Info().Strs("allowed_senders", have).Msg("ttc flow: cellular allowed_senders extended, gateway restarted")
	return true, nil
}

// ttcStatus builds the response.
func (s *Server) ttcStatus(extraIssues []string) ttcFlowStatus {
	peer, hub := s.ttcNumbers()
	st := ttcFlowStatus{Available: ttcPaths, Rules: map[string]ttcFlowRule{}, PeerNumber: peer, HubNumber: hub, Issues: extraIssues}
	if v, err := s.db.GetSystemConfig(ttcFlowKey); err == nil && ttcPathValid(v) {
		st.Path = v
	}
	rules, _, err := s.ttcRules()
	if err != nil {
		st.Issues = append(st.Issues, err.Error())
		return st
	}
	enabled := 0
	for _, p := range ttcPaths {
		r := rules[p]
		if r == nil {
			st.Issues = append(st.Issues, "rule ttc:"+p+" is missing (run setup)")
			continue
		}
		fr := ttcFlowRule{ID: r.ID, Enabled: r.Enabled, ForwardTo: r.ForwardTo}
		if p != "aprs" {
			fr.Contact = s.ttcContactPhone(r)
		}
		st.Rules[p] = fr
		if r.Enabled {
			enabled++
			if st.Path == "" {
				st.Path = p
			}
		}
	}
	if enabled > 1 {
		st.Issues = append(st.Issues, "more than one ttc rule is enabled; a text would leave twice")
	}
	st.Ready = len(st.Issues) == 0 && len(st.Rules) == len(ttcPaths) && st.Path != ""
	return st
}

// handleGetTTCFlow returns the selected booth path.
// @Summary Booth flow: current path
// @Description Which of the three booth paths (aprs, b2b_sms, hub_sms) this kit uses for the next message typed on its handheld, with the backing rules and readiness. Per kit, egress only. [MESHSAT-962]
// @Tags ttc
// @Produce json
// @Success 200 {object} ttcFlowStatus
// @Router /api/ttc/flow [get]
func (s *Server) handleGetTTCFlow(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.ttcStatus(nil))
}

// handlePutTTCFlow selects the booth path.
// @Summary Booth flow: select the path
// @Description Enables exactly one of the three "ttc:<path>" egress rules on mesh_0 and disables the other two, persists the choice in system_config and reloads the rules engine. Creates missing rules first when the numbers are known. [MESHSAT-962]
// @Tags ttc
// @Accept json
// @Produce json
// @Param body body object true "Path" example({"path":"hub_sms"})
// @Success 200 {object} ttcFlowStatus
// @Failure 400 {object} map[string]string
// @Failure 500 {object} map[string]string
// @Router /api/ttc/flow [put]
func (s *Server) handlePutTTCFlow(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if !ttcPathValid(req.Path) {
		writeError(w, http.StatusBadRequest, "path must be one of aprs, b2b_sms, hub_sms")
		return
	}
	peer, hub := s.ttcNumbers()
	rules, issues, err := s.ttcEnsureRules(peer, hub)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if rules[req.Path] == nil {
		writeError(w, http.StatusBadRequest, "path "+req.Path+" has no rule: "+strings.Join(issues, "; "))
		return
	}
	for p, rule := range rules {
		want := p == req.Path
		if rule.Enabled != want {
			if err := s.db.SetAccessRuleEnabled(rule.ID, want); err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
			rule.Enabled = want
		}
	}
	if err := s.db.SetSystemConfig(ttcFlowKey, req.Path); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.reloadAccessRules()
	log.Info().Str("path", req.Path).Msg("ttc flow: path selected")
	writeJSON(w, http.StatusOK, s.ttcStatus(issues))
}

// handleTTCFlowSetup prepares a kit for the booth flow selector.
// @Summary Booth flow: one-time setup
// @Description Stores the peer kit's and the Hub's SMS numbers, creates the SMS contacts and the three "ttc:<path>" egress rules (adopting an existing mesh_0 -> peer_link rule as ttc:aprs), enables the aprs_0 and cellular_0 -> mesh_0 inbound rules, and adds both numbers to cellular_0 allowed_senders (restarts the cellular gateway only when a number was missing). Idempotent. [MESHSAT-962]
// @Tags ttc
// @Accept json
// @Produce json
// @Param body body object true "Numbers" example({"peer_number":"+31653207829","hub_number":"+3197010258258"})
// @Success 200 {object} ttcFlowStatus
// @Failure 400 {object} map[string]string
// @Failure 500 {object} map[string]string
// @Router /api/ttc/flow/setup [post]
func (s *Server) handleTTCFlowSetup(w http.ResponseWriter, r *http.Request) {
	var req struct {
		PeerNumber string `json:"peer_number"`
		HubNumber  string `json:"hub_number"`
	}
	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err.Error() != "EOF" {
			writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
	}
	for key, val := range map[string]string{ttcPeerNumberKey: req.PeerNumber, ttcHubNumberKey: req.HubNumber} {
		val = strings.TrimSpace(val)
		if val == "" {
			continue
		}
		if !strings.HasPrefix(val, "+") {
			writeError(w, http.StatusBadRequest, "numbers must be in +country format: "+val)
			return
		}
		if err := s.db.SetSystemConfig(key, val); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	peer, hub := s.ttcNumbers()
	_, issues, err := s.ttcEnsureRules(peer, hub)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := s.ttcEnsureInbound(); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if restarted, err := s.ttcEnsureAllowedSenders(r.Context(), peer, hub); err != nil {
		issues = append(issues, "allowed_senders: "+err.Error())
	} else if restarted {
		issues = append(issues, "cellular gateway restarted to extend allowed_senders; the modem answers again in about 20 s")
	}
	s.reloadAccessRules()
	writeJSON(w, http.StatusOK, s.ttcStatus(issues))
}
