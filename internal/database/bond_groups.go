package database

import "fmt"

// BondGroup defines a bonding group for multi-path delivery across interfaces.
type BondGroup struct {
	ID             string  `db:"id" json:"id"`
	Label          string  `db:"label" json:"label"`
	CostBudget     float64 `db:"cost_budget" json:"cost_budget"`
	MinReliability float64 `db:"min_reliability" json:"min_reliability"`
	CreatedAt      string  `db:"created_at" json:"created_at"`
	UpdatedAt      string  `db:"updated_at" json:"updated_at"`
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

// UpdateBondGroup updates a bond group's label, cost budget, and min reliability.
func (db *DB) UpdateBondGroup(g *BondGroup) error {
	_, err := db.Exec(`UPDATE bond_groups SET label = ?, cost_budget = ?, min_reliability = ?, updated_at = datetime('now') WHERE id = ?`,
		g.Label, g.CostBudget, g.MinReliability, g.ID)
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

// InsertBondMember adds an interface to a bond group.
func (db *DB) InsertBondMember(m *BondMember) error {
	_, err := db.Exec(`INSERT INTO bond_members (group_id, interface_id, priority) VALUES (?, ?, ?)`,
		m.GroupID, m.InterfaceID, m.Priority)
	if err != nil {
		return fmt.Errorf("insert bond member: %w", err)
	}
	return nil
}

// DeleteBondMember removes a bond member by ID.
func (db *DB) DeleteBondMember(id int64) error {
	_, err := db.Exec("DELETE FROM bond_members WHERE id = ?", id)
	return err
}

// DeleteBondMembers removes all members from a bond group.
func (db *DB) DeleteBondMembers(groupID string) error {
	_, err := db.Exec("DELETE FROM bond_members WHERE group_id = ?", groupID)
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
