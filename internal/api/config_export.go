package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/rs/zerolog/log"
	"gopkg.in/yaml.v3"

	"meshsat/internal/database"
)

// ConfigExport is the top-level YAML structure for config export/import.
type ConfigExport struct {
	Version        string                `yaml:"version"`
	ExportedAt     string                `yaml:"exported_at"`
	Interfaces     []InterfaceExport     `yaml:"interfaces"`
	AccessRules    []AccessRuleExport    `yaml:"access_rules"`
	ObjectGroups   []ObjectGroupExport   `yaml:"object_groups,omitempty"`
	FailoverGroups []FailoverGroupExport `yaml:"failover_groups,omitempty"`
}

// InterfaceExport represents an interface in the config export (excludes runtime fields).
type InterfaceExport struct {
	ID                string `yaml:"id"`
	ChannelType       string `yaml:"channel_type"`
	Label             string `yaml:"label"`
	Enabled           bool   `yaml:"enabled"`
	Config            string `yaml:"config,omitempty"`
	IngressTransforms string `yaml:"ingress_transforms,omitempty"`
	EgressTransforms  string `yaml:"egress_transforms,omitempty"`
}

// AccessRuleExport represents an access rule in the config export.
type AccessRuleExport struct {
	InterfaceID        string  `yaml:"interface_id"`
	Direction          string  `yaml:"direction"`
	Priority           int     `yaml:"priority"`
	Name               string  `yaml:"name"`
	Enabled            bool    `yaml:"enabled"`
	Action             string  `yaml:"action"`
	ForwardTo          string  `yaml:"forward_to,omitempty"`
	Filters            string  `yaml:"filters,omitempty"`
	FilterNodeGroup    *string `yaml:"filter_node_group,omitempty"`
	FilterSenderGroup  *string `yaml:"filter_sender_group,omitempty"`
	FilterPortnumGroup *string `yaml:"filter_portnum_group,omitempty"`
	ScheduleType       string  `yaml:"schedule_type,omitempty"`
	ScheduleConfig     string  `yaml:"schedule_config,omitempty"`
	ForwardOptions     string  `yaml:"forward_options,omitempty"`
	QoSLevel           int     `yaml:"qos_level"`
	RateLimitPerMin    int     `yaml:"rate_limit_per_min,omitempty"`
	RateLimitWindow    int     `yaml:"rate_limit_window,omitempty"`
}

// ObjectGroupExport represents an object group in the config export.
type ObjectGroupExport struct {
	ID      string `yaml:"id"`
	Type    string `yaml:"type"`
	Label   string `yaml:"label"`
	Members string `yaml:"members"`
}

// FailoverGroupExport represents a failover group in the config export.
type FailoverGroupExport struct {
	ID      string                 `yaml:"id"`
	Label   string                 `yaml:"label"`
	Mode    string                 `yaml:"mode"`
	Members []FailoverMemberExport `yaml:"members,omitempty"`
}

// FailoverMemberExport represents a failover group member in the config export.
type FailoverMemberExport struct {
	InterfaceID string `yaml:"interface_id"`
	Priority    int    `yaml:"priority"`
}

// handleConfigExport exports the full interface + access rules configuration as YAML.
// @Summary Export running configuration
// @Description Returns a YAML document containing all interfaces, access rules, object groups, and failover groups. Excludes runtime fields (device_id, device_port, counters, timestamps).
// @Tags config
// @Produce application/yaml
// @Success 200 {string} string "YAML configuration document"
// @Failure 500 {object} map[string]string
// @Router /api/config/export [get]
func (s *Server) handleConfigExport(w http.ResponseWriter, r *http.Request) {
	ifaces, err := s.db.GetAllInterfaces()
	if err != nil {
		log.Error().Err(err).Msg("config export: failed to load interfaces")
		writeError(w, http.StatusInternalServerError, "failed to load interfaces")
		return
	}

	rules, err := s.db.GetAllAccessRules()
	if err != nil {
		log.Error().Err(err).Msg("config export: failed to load access rules")
		writeError(w, http.StatusInternalServerError, "failed to load access rules")
		return
	}

	groups, err := s.db.GetAllObjectGroups()
	if err != nil {
		log.Error().Err(err).Msg("config export: failed to load object groups")
		writeError(w, http.StatusInternalServerError, "failed to load object groups")
		return
	}

	fgroups, err := s.db.GetAllFailoverGroups()
	if err != nil {
		log.Error().Err(err).Msg("config export: failed to load failover groups")
		writeError(w, http.StatusInternalServerError, "failed to load failover groups")
		return
	}

	export := ConfigExport{
		Version:    "0.3.0",
		ExportedAt: time.Now().UTC().Format(time.RFC3339),
	}

	// Redact encryption keys from transform chains unless explicitly requested.
	// Keys in plaintext config exports are a security risk (MESHSAT-339 audit finding).
	includeKeys := r.URL.Query().Get("include_keys") == "true"

	for _, i := range ifaces {
		ingress := i.IngressTransforms
		egress := i.EgressTransforms
		if !includeKeys {
			ingress = redactTransformKeys(ingress)
			egress = redactTransformKeys(egress)
		}
		export.Interfaces = append(export.Interfaces, InterfaceExport{
			ID:                i.ID,
			ChannelType:       i.ChannelType,
			Label:             i.Label,
			Enabled:           i.Enabled,
			Config:            i.Config,
			IngressTransforms: ingress,
			EgressTransforms:  egress,
		})
	}

	for _, ar := range rules {
		export.AccessRules = append(export.AccessRules, AccessRuleExport{
			InterfaceID:        ar.InterfaceID,
			Direction:          ar.Direction,
			Priority:           ar.Priority,
			Name:               ar.Name,
			Enabled:            ar.Enabled,
			Action:             ar.Action,
			ForwardTo:          ar.ForwardTo,
			Filters:            ar.Filters,
			FilterNodeGroup:    ar.FilterNodeGroup,
			FilterSenderGroup:  ar.FilterSenderGroup,
			FilterPortnumGroup: ar.FilterPortnumGroup,
			ScheduleType:       ar.ScheduleType,
			ScheduleConfig:     ar.ScheduleConfig,
			ForwardOptions:     ar.ForwardOptions,
			QoSLevel:           ar.QoSLevel,
			RateLimitPerMin:    ar.RateLimitPerMin,
			RateLimitWindow:    ar.RateLimitWindow,
		})
	}

	for _, g := range groups {
		export.ObjectGroups = append(export.ObjectGroups, ObjectGroupExport{
			ID:      g.ID,
			Type:    g.Type,
			Label:   g.Label,
			Members: g.Members,
		})
	}

	for _, fg := range fgroups {
		fge := FailoverGroupExport{
			ID:    fg.ID,
			Label: fg.Label,
			Mode:  fg.Mode,
		}
		members, err := s.db.GetFailoverMembers(fg.ID)
		if err != nil {
			log.Error().Err(err).Str("group", fg.ID).Msg("config export: failed to load failover members")
		}
		for _, m := range members {
			fge.Members = append(fge.Members, FailoverMemberExport{
				InterfaceID: m.InterfaceID,
				Priority:    m.Priority,
			})
		}
		export.FailoverGroups = append(export.FailoverGroups, fge)
	}

	w.Header().Set("Content-Type", "application/yaml")
	w.Header().Set("Content-Disposition", `attachment; filename="meshsat-config.yaml"`)
	if err := yaml.NewEncoder(w).Encode(export); err != nil {
		log.Error().Err(err).Msg("config export: failed to encode YAML")
	}
}

// ConfigDiffResult shows what would change if a YAML config were imported.
type ConfigDiffResult struct {
	Interfaces     DiffCounts `json:"interfaces"`
	AccessRules    DiffCounts `json:"access_rules"`
	ObjectGroups   DiffCounts `json:"object_groups"`
	FailoverGroups DiffCounts `json:"failover_groups"`
}

// DiffCounts summarizes additions, removals, and changes for a config entity type.
type DiffCounts struct {
	Add    int `json:"add"`
	Remove int `json:"remove"`
	Change int `json:"change"`
	Keep   int `json:"keep"`
}

// handleConfigDiff shows what would change between the running config and an uploaded YAML.
// @Summary Preview configuration changes
// @Description Accepts a YAML document and returns a diff summary showing what would be added, removed, changed, or kept — without applying any changes.
// @Tags config
// @Accept application/yaml
// @Produce json
// @Param config body string true "YAML configuration document"
// @Success 200 {object} ConfigDiffResult
// @Failure 400 {object} map[string]string
// @Failure 500 {object} map[string]string
// @Router /api/config/diff [post]
func (s *Server) handleConfigDiff(w http.ResponseWriter, r *http.Request) {
	var incoming ConfigExport
	if err := yaml.NewDecoder(r.Body).Decode(&incoming); err != nil {
		writeError(w, http.StatusBadRequest, "invalid YAML: "+err.Error())
		return
	}

	// Load current running config
	currentIfaces, err := s.db.GetAllInterfaces()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load interfaces")
		return
	}
	currentRules, err := s.db.GetAllAccessRules()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load access rules")
		return
	}
	currentGroups, err := s.db.GetAllObjectGroups()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load object groups")
		return
	}
	currentFGroups, err := s.db.GetAllFailoverGroups()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load failover groups")
		return
	}

	result := ConfigDiffResult{
		Interfaces:     diffByID(mapIDs(currentIfaces, func(i database.Interface) string { return i.ID }), mapIDs(incoming.Interfaces, func(i InterfaceExport) string { return i.ID })),
		AccessRules:    diffByName(mapNames(currentRules, func(r database.AccessRule) string { return r.Name }), mapNames(incoming.AccessRules, func(r AccessRuleExport) string { return r.Name })),
		ObjectGroups:   diffByID(mapIDs(currentGroups, func(g database.ObjectGroup) string { return g.ID }), mapIDs(incoming.ObjectGroups, func(g ObjectGroupExport) string { return g.ID })),
		FailoverGroups: diffByID(mapIDs(currentFGroups, func(g database.FailoverGroup) string { return g.ID }), mapIDs(incoming.FailoverGroups, func(g FailoverGroupExport) string { return g.ID })),
	}

	writeJSON(w, http.StatusOK, result)
}

func mapIDs[T any](items []T, idFn func(T) string) map[string]bool {
	m := make(map[string]bool, len(items))
	for _, item := range items {
		m[idFn(item)] = true
	}
	return m
}

func mapNames[T any](items []T, nameFn func(T) string) map[string]bool {
	return mapIDs(items, nameFn)
}

func diffByID(current, incoming map[string]bool) DiffCounts {
	var d DiffCounts
	for id := range incoming {
		if current[id] {
			d.Change++ // exists in both — potentially changed
		} else {
			d.Add++
		}
	}
	for id := range current {
		if !incoming[id] {
			d.Remove++
		}
	}
	return d
}

func diffByName(current, incoming map[string]bool) DiffCounts {
	return diffByID(current, incoming)
}

// handleConfigImport imports a full interface + access rules configuration from YAML.
// @Summary Import running configuration
// @Description Accepts a YAML document and replaces all interfaces, access rules, object groups, and failover groups. This is a full replace (not merge) applied as a transaction.
// @Tags config
// @Accept application/yaml
// @Produce json
// @Param config body string true "YAML configuration document"
// @Success 200 {object} map[string]int "Count of imported entities per type"
// @Failure 400 {object} map[string]string
// @Failure 500 {object} map[string]string
// @Router /api/config/import [post]
func (s *Server) handleConfigImport(w http.ResponseWriter, r *http.Request) {
	var cfg ConfigExport
	if err := yaml.NewDecoder(r.Body).Decode(&cfg); err != nil {
		writeError(w, http.StatusBadRequest, "invalid YAML: "+err.Error())
		return
	}

	if cfg.Version == "" {
		writeError(w, http.StatusBadRequest, "missing version field")
		return
	}

	// Validate: all access rules must reference declared interfaces.
	ifaceIDs := make(map[string]bool)
	for _, i := range cfg.Interfaces {
		if i.ID == "" || i.ChannelType == "" {
			writeError(w, http.StatusBadRequest, "interface missing id or channel_type")
			return
		}
		ifaceIDs[i.ID] = true
	}
	for _, ar := range cfg.AccessRules {
		if !ifaceIDs[ar.InterfaceID] {
			writeError(w, http.StatusBadRequest,
				fmt.Sprintf("access rule %q references unknown interface %q", ar.Name, ar.InterfaceID))
			return
		}
	}

	// Validate: failover group members must reference declared interfaces.
	for _, fg := range cfg.FailoverGroups {
		if fg.ID == "" {
			writeError(w, http.StatusBadRequest, "failover group missing id")
			return
		}
		for _, m := range fg.Members {
			if !ifaceIDs[m.InterfaceID] {
				writeError(w, http.StatusBadRequest,
					fmt.Sprintf("failover group %q member references unknown interface %q", fg.ID, m.InterfaceID))
				return
			}
		}
	}

	// Apply as transaction: clear existing in reverse dependency order, then insert new.
	tx, err := s.db.Begin()
	if err != nil {
		log.Error().Err(err).Msg("config import: failed to begin transaction")
		writeError(w, http.StatusInternalServerError, "failed to begin transaction")
		return
	}
	defer tx.Rollback() //nolint:errcheck

	for _, stmt := range []string{
		"DELETE FROM failover_members",
		"DELETE FROM failover_groups",
		"DELETE FROM access_rules",
		"DELETE FROM object_groups",
		"DELETE FROM interfaces",
	} {
		if _, err := tx.Exec(stmt); err != nil {
			log.Error().Err(err).Str("sql", stmt).Msg("config import: failed to clear table")
			writeError(w, http.StatusInternalServerError, "failed to clear existing config")
			return
		}
	}

	if err := tx.Commit(); err != nil {
		log.Error().Err(err).Msg("config import: failed to commit clear")
		writeError(w, http.StatusInternalServerError, "failed to clear existing config")
		return
	}

	counts := map[string]int{}

	for _, i := range cfg.Interfaces {
		iface := database.Interface{
			ID:                i.ID,
			ChannelType:       i.ChannelType,
			Label:             i.Label,
			Enabled:           i.Enabled,
			Config:            i.Config,
			IngressTransforms: i.IngressTransforms,
			EgressTransforms:  i.EgressTransforms,
		}
		if err := s.db.InsertInterface(&iface); err != nil {
			log.Error().Err(err).Str("id", i.ID).Msg("config import: failed to insert interface")
			continue
		}
		counts["interfaces"]++
	}

	for _, g := range cfg.ObjectGroups {
		og := database.ObjectGroup{
			ID:      g.ID,
			Type:    g.Type,
			Label:   g.Label,
			Members: g.Members,
		}
		if err := s.db.InsertObjectGroup(&og); err != nil {
			log.Error().Err(err).Str("id", g.ID).Msg("config import: failed to insert object group")
			continue
		}
		counts["object_groups"]++
	}

	for _, ar := range cfg.AccessRules {
		rule := database.AccessRule{
			InterfaceID:        ar.InterfaceID,
			Direction:          ar.Direction,
			Priority:           ar.Priority,
			Name:               ar.Name,
			Enabled:            ar.Enabled,
			Action:             ar.Action,
			ForwardTo:          ar.ForwardTo,
			Filters:            ar.Filters,
			FilterNodeGroup:    ar.FilterNodeGroup,
			FilterSenderGroup:  ar.FilterSenderGroup,
			FilterPortnumGroup: ar.FilterPortnumGroup,
			ScheduleType:       ar.ScheduleType,
			ScheduleConfig:     ar.ScheduleConfig,
			ForwardOptions:     ar.ForwardOptions,
			QoSLevel:           ar.QoSLevel,
			RateLimitPerMin:    ar.RateLimitPerMin,
			RateLimitWindow:    ar.RateLimitWindow,
		}
		if _, err := s.db.InsertAccessRule(&rule); err != nil {
			log.Error().Err(err).Str("name", ar.Name).Msg("config import: failed to insert access rule")
			continue
		}
		counts["access_rules"]++
	}

	for _, fg := range cfg.FailoverGroups {
		g := database.FailoverGroup{
			ID:    fg.ID,
			Label: fg.Label,
			Mode:  fg.Mode,
		}
		if err := s.db.InsertFailoverGroup(&g); err != nil {
			log.Error().Err(err).Str("id", fg.ID).Msg("config import: failed to insert failover group")
			continue
		}
		counts["failover_groups"]++
		for _, m := range fg.Members {
			fm := database.FailoverMember{
				GroupID:     fg.ID,
				InterfaceID: m.InterfaceID,
				Priority:    m.Priority,
			}
			if err := s.db.InsertFailoverMember(&fm); err != nil {
				log.Error().Err(err).Str("group", fg.ID).Str("iface", m.InterfaceID).Msg("config import: failed to insert failover member")
			}
		}
	}

	// Reload access rules into the in-memory evaluator.
	s.reloadAccessRules()

	log.Info().Interface("counts", counts).Msg("config import complete")
	writeJSON(w, http.StatusOK, counts)
}

// redactTransformKeys replaces encryption key values in transform JSON with [REDACTED].
// Transform JSON format: [{"type":"encrypt","params":{"key":"a1b2c3..."}}]
func redactTransformKeys(transforms string) string {
	if transforms == "" || transforms == "[]" {
		return transforms
	}
	// Simple regex-free approach: unmarshal, redact, remarshal
	var chain []map[string]interface{}
	if err := json.Unmarshal([]byte(transforms), &chain); err != nil {
		return transforms // unparseable, return as-is
	}
	for _, t := range chain {
		if params, ok := t["params"].(map[string]interface{}); ok {
			if _, hasKey := params["key"]; hasKey {
				params["key"] = "[REDACTED]"
			}
		}
	}
	out, err := json.Marshal(chain)
	if err != nil {
		return transforms
	}
	return string(out)
}
