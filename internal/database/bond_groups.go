package database

import (
	"fmt"
	"strings"
)

// BearerShortLabel maps a Reticulum interface_id to the short,
// human-facing bearer name used in bond-group auto-labels and in the
// operator dashboard's Active Comms tile. Kept in sync with the same
// mapping in web/src/views/DashboardView.vue :: bearerShortLabel —
// if you add a new bearer type there, mirror it here. [MESHSAT-687]
func BearerShortLabel(id string) string {
	switch {
	case id == "":
		return ""
	case strings.HasPrefix(id, "mesh_"):
		return "Mesh"
	case strings.HasPrefix(id, "ax25_"):
		return "APRS"
	case strings.HasPrefix(id, "iridium_imt"):
		return "IMT"
	case strings.HasPrefix(id, "iridium_"):
		return "SBD"
	case strings.HasPrefix(id, "sms_"):
		return "SMS"
	case strings.HasPrefix(id, "cellular_"):
		return "Cell"
	case strings.HasPrefix(id, "tcp_"):
		return "TCP"
	case strings.HasPrefix(id, "ble_"):
		return "BLE"
	case strings.HasPrefix(id, "zigbee_"):
		return "ZB"
	case strings.HasPrefix(id, "mqtt_rns_"):
		return "MQTT"
	}
	return id
}

// recomputeBondLabel rewrites the bond_groups.label column from the
// current members list IF the existing label looks auto-generated (or
// is empty). Human-authored labels — anything with a space or that
// doesn't match the "Part+Part+Part" pattern — are preserved so
// renaming "bond1" to "Mission Charlie" isn't clobbered by the next
// membership change. [MESHSAT-687] Silent no-op when the group has
// already been deleted.
func (db *DB) recomputeBondLabel(groupID string) {
	if groupID == "" {
		return
	}
	var existing string
	if err := db.QueryRow("SELECT label FROM bond_groups WHERE id = ?", groupID).Scan(&existing); err != nil {
		return
	}
	if existing != "" && !looksAutoBondLabel(existing) {
		return // preserve human-authored label
	}
	members, err := db.GetBondMembers(groupID)
	if err != nil {
		return
	}
	parts := make([]string, 0, len(members))
	for _, m := range members {
		s := BearerShortLabel(m.InterfaceID)
		if s != "" {
			parts = append(parts, s)
		}
	}
	label := strings.Join(parts, "+")
	if label == "" {
		label = "(empty)"
	}
	if label == existing {
		return
	}
	_, _ = db.Exec(
		`UPDATE bond_groups SET label = ?, updated_at = datetime('now') WHERE id = ?`,
		label, groupID,
	)
}

// looksAutoBondLabel returns true when a label matches the auto-
// generated shape — `BearerShort[+BearerShort...]` with no spaces,
// no punctuation beyond `+`, and each part starting with an uppercase
// letter. Anything that doesn't match is assumed to be operator-
// written and must not be overwritten by recomputeBondLabel. Also
// treats "(empty)" (our auto-generated placeholder for a bond with
// no members) as auto so adding a first member re-labels cleanly.
// [MESHSAT-687]
func looksAutoBondLabel(s string) bool {
	if s == "" || s == "(empty)" {
		return true
	}
	if strings.ContainsAny(s, " \t,./:;") {
		return false
	}
	for _, part := range strings.Split(s, "+") {
		if part == "" {
			return false
		}
		first := part[0]
		if first < 'A' || first > 'Z' {
			return false
		}
		for i := 1; i < len(part); i++ {
			c := part[i]
			isLetter := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
			isDigit := c >= '0' && c <= '9'
			if !isLetter && !isDigit {
				return false
			}
		}
	}
	return true
}

// BondGroup defines a bonding group for multi-path delivery across interfaces.
//
// EgressTransforms and IngressTransforms carry the **bond-level**
// transform chain (JSON, same shape as interfaces.{egress,ingress}_transforms).
// When set, the dispatcher runs `ApplyEgress` on the payload BEFORE
// feeding it to the HeMB bonder, so HeMB codes the resulting ciphertext
// across all bearers. The receiver applies `ApplyIngress` to the
// reconstructed payload once HeMB has gathered a K-of-N quorum.
// This is the encrypt-then-code ordering required for AES-GCM to
// compose with GF(256) erasure coding. [MESHSAT-664]
type BondGroup struct {
	ID                string  `db:"id" json:"id"`
	Label             string  `db:"label" json:"label"`
	CostBudget        float64 `db:"cost_budget" json:"cost_budget"`
	MinReliability    float64 `db:"min_reliability" json:"min_reliability"`
	EgressTransforms  string  `db:"egress_transforms" json:"egress_transforms"`
	IngressTransforms string  `db:"ingress_transforms" json:"ingress_transforms"`
	CreatedAt         string  `db:"created_at" json:"created_at"`
	UpdatedAt         string  `db:"updated_at" json:"updated_at"`
}

// BondMember maps an interface into a bond group with priority ordering.
type BondMember struct {
	ID          int64  `db:"id" json:"id"`
	GroupID     string `db:"group_id" json:"group_id"`
	InterfaceID string `db:"interface_id" json:"interface_id"`
	Priority    int    `db:"priority" json:"priority"`
	CreatedAt   string `db:"created_at" json:"created_at"`
}

// GetBondGroup returns a single bond group by ID.
func (db *DB) GetBondGroup(id string) (*BondGroup, error) {
	var g BondGroup
	if err := db.Get(&g, "SELECT * FROM bond_groups WHERE id = ?", id); err != nil {
		return nil, err
	}
	return &g, nil
}

// GetAllBondGroups returns all bond groups.
func (db *DB) GetAllBondGroups() ([]BondGroup, error) {
	var groups []BondGroup
	if err := db.Select(&groups, "SELECT * FROM bond_groups ORDER BY id"); err != nil {
		return nil, fmt.Errorf("query bond groups: %w", err)
	}
	return groups, nil
}

// InsertBondGroup creates a new bond group.
func (db *DB) InsertBondGroup(g *BondGroup) error {
	_, err := db.Exec(`INSERT INTO bond_groups (id, label, cost_budget, min_reliability) VALUES (?, ?, ?, ?)`,
		g.ID, g.Label, g.CostBudget, g.MinReliability)
	if err != nil {
		return fmt.Errorf("insert bond group: %w", err)
	}
	return nil
}

// UpdateBondGroup updates a bond group's label, cost budget, min
// reliability, and transform chains. Passing an empty-string for a
// transforms field resets it to the `[]` default.
func (db *DB) UpdateBondGroup(g *BondGroup) error {
	eg := g.EgressTransforms
	if eg == "" {
		eg = "[]"
	}
	ig := g.IngressTransforms
	if ig == "" {
		ig = "[]"
	}
	_, err := db.Exec(`UPDATE bond_groups
		SET label = ?, cost_budget = ?, min_reliability = ?,
		    egress_transforms = ?, ingress_transforms = ?,
		    updated_at = datetime('now')
		WHERE id = ?`,
		g.Label, g.CostBudget, g.MinReliability, eg, ig, g.ID)
	if err != nil {
		return fmt.Errorf("update bond group: %w", err)
	}
	return nil
}

// DeleteBondGroup removes a bond group by ID. Members are cascade-deleted.
func (db *DB) DeleteBondGroup(id string) error {
	_, err := db.Exec("DELETE FROM bond_groups WHERE id = ?", id)
	return err
}

// GetBondMembers returns all members of a bond group ordered by priority.
func (db *DB) GetBondMembers(groupID string) ([]BondMember, error) {
	var members []BondMember
	if err := db.Select(&members,
		"SELECT * FROM bond_members WHERE group_id = ? ORDER BY priority ASC", groupID); err != nil {
		return nil, fmt.Errorf("query bond members: %w", err)
	}
	return members, nil
}

// InsertBondMember adds an interface to a bond group, then auto-syncs
// the bond's label from the live members so it doesn't drift from
// reality (e.g. "Mesh+APRS" becoming "Mesh+APRS+TCP" after the
// WiFi-P2P reconciler enrols tcp_0). [MESHSAT-687]
func (db *DB) InsertBondMember(m *BondMember) error {
	_, err := db.Exec(`INSERT INTO bond_members (group_id, interface_id, priority) VALUES (?, ?, ?)`,
		m.GroupID, m.InterfaceID, m.Priority)
	if err != nil {
		return fmt.Errorf("insert bond member: %w", err)
	}
	db.recomputeBondLabel(m.GroupID)
	return nil
}

// DeleteBondMember removes a bond member by ID. We have to look up the
// group_id BEFORE the delete because after it the row is gone; then
// recompute the label on that group so it reflects the remaining
// members. [MESHSAT-687]
func (db *DB) DeleteBondMember(id int64) error {
	var groupID string
	_ = db.QueryRow("SELECT group_id FROM bond_members WHERE id = ?", id).Scan(&groupID)
	_, err := db.Exec("DELETE FROM bond_members WHERE id = ?", id)
	if err == nil && groupID != "" {
		db.recomputeBondLabel(groupID)
	}
	return err
}

// DeleteBondMembers removes all members from a bond group. Recomputed
// label after the wipe will be "(empty)" so the widget doesn't show a
// stale bearer list. [MESHSAT-687]
func (db *DB) DeleteBondMembers(groupID string) error {
	_, err := db.Exec("DELETE FROM bond_members WHERE group_id = ?", groupID)
	if err == nil {
		db.recomputeBondLabel(groupID)
	}
	return err
}

// IsBondGroup returns true if the given ID is a bond group.
func (db *DB) IsBondGroup(id string) bool {
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM bond_groups WHERE id = ?", id).Scan(&count); err != nil {
		return false
	}
	return count > 0
}
