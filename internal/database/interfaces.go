package database

import "fmt"

// ---- v0.3.0 Interface Model Types ----

// Interface represents a named communication channel (mesh, iridium, astrocast, cellular, webhook, mqtt).
type Interface struct {
	ID                string `db:"id" json:"id"`
	ChannelType       string `db:"channel_type" json:"channel_type"`
	Label             string `db:"label" json:"label"`
	Enabled           bool   `db:"enabled" json:"enabled"`
	DeviceID          string `db:"device_id" json:"device_id"`
	DevicePort        string `db:"device_port" json:"device_port"`
	Config            string `db:"config" json:"config"`
	IngressTransforms string `db:"ingress_transforms" json:"ingress_transforms"`
	EgressTransforms  string `db:"egress_transforms" json:"egress_transforms"`
	IngressSeq        int64  `db:"ingress_seq" json:"ingress_seq"`
	EgressSeq         int64  `db:"egress_seq" json:"egress_seq"`
	CreatedAt         string `db:"created_at" json:"created_at"`
	UpdatedAt         string `db:"updated_at" json:"updated_at"`
}

// ObjectGroup represents a reusable set of nodes, senders, or portnums for access rules.
type ObjectGroup struct {
	ID        string `db:"id" json:"id"`
	Type      string `db:"type" json:"type"`
	Label     string `db:"label" json:"label"`
	Members   string `db:"members" json:"members"`
	CreatedAt string `db:"created_at" json:"created_at"`
}

// AccessRule represents a directional filtering/forwarding rule bound to an interface.
type AccessRule struct {
	ID                 int64   `db:"id" json:"id"`
	InterfaceID        string  `db:"interface_id" json:"interface_id"`
	Direction          string  `db:"direction" json:"direction"`
	Priority           int     `db:"priority" json:"priority"`
	Name               string  `db:"name" json:"name"`
	Enabled            bool    `db:"enabled" json:"enabled"`
	Action             string  `db:"action" json:"action"`
	ForwardTo          string  `db:"forward_to" json:"forward_to"`
	Filters            string  `db:"filters" json:"filters"`
	FilterNodeGroup    *string `db:"filter_node_group" json:"filter_node_group,omitempty"`
	FilterSenderGroup  *string `db:"filter_sender_group" json:"filter_sender_group,omitempty"`
	FilterPortnumGroup *string `db:"filter_portnum_group" json:"filter_portnum_group,omitempty"`
	ScheduleType       string  `db:"schedule_type" json:"schedule_type"`
	ScheduleConfig     string  `db:"schedule_config" json:"schedule_config"`
	ForwardOptions     string  `db:"forward_options" json:"forward_options"`
	QoSLevel           int     `db:"qos_level" json:"qos_level"`
	RateLimitPerMin    int     `db:"rate_limit_per_min" json:"rate_limit_per_min"`
	RateLimitWindow    int     `db:"rate_limit_window" json:"rate_limit_window"`
	MatchCount         int64   `db:"match_count" json:"match_count"`
	LastMatchAt        *string `db:"last_match_at" json:"last_match_at,omitempty"`
	CreatedAt          string  `db:"created_at" json:"created_at"`
	UpdatedAt          string  `db:"updated_at" json:"updated_at"`
}

// FailoverGroup represents a group of interfaces with priority-based failover or load balancing.
type FailoverGroup struct {
	ID        string `db:"id" json:"id"`
	Label     string `db:"label" json:"label"`
	Mode      string `db:"mode" json:"mode"`
	CreatedAt string `db:"created_at" json:"created_at"`
}

// FailoverMember represents a single interface within a failover group.
type FailoverMember struct {
	ID          int64  `db:"id" json:"id"`
	GroupID     string `db:"group_id" json:"group_id"`
	InterfaceID string `db:"interface_id" json:"interface_id"`
	Priority    int    `db:"priority" json:"priority"`
	CreatedAt   string `db:"created_at" json:"created_at"`
}

// AuditLogEntry represents a tamper-evident log entry for interface operations.
type AuditLogEntry struct {
	ID          int64   `db:"id" json:"id"`
	Timestamp   string  `db:"timestamp" json:"timestamp"`
	InterfaceID *string `db:"interface_id" json:"interface_id,omitempty"`
	Direction   *string `db:"direction" json:"direction,omitempty"`
	EventType   string  `db:"event_type" json:"event_type"`
	SeqNum      *int64  `db:"seq_num" json:"seq_num,omitempty"`
	DeliveryID  *int64  `db:"delivery_id" json:"delivery_id,omitempty"`
	RuleID      *int64  `db:"rule_id" json:"rule_id,omitempty"`
	Detail      string  `db:"detail" json:"detail"`
	PrevHash    string  `db:"prev_hash" json:"prev_hash"`
	Hash        string  `db:"hash" json:"hash"`
}

// ---- Interface CRUD ----

// GetInterface returns a single interface by ID.
func (db *DB) GetInterface(id string) (*Interface, error) {
	var iface Interface
	if err := db.Get(&iface, "SELECT * FROM interfaces WHERE id = ?", id); err != nil {
		return nil, err
	}
	return &iface, nil
}

// GetAllInterfaces returns all interfaces ordered by channel_type and id.
func (db *DB) GetAllInterfaces() ([]Interface, error) {
	var ifaces []Interface
	if err := db.Select(&ifaces, "SELECT * FROM interfaces ORDER BY channel_type, id"); err != nil {
		return nil, fmt.Errorf("query interfaces: %w", err)
	}
	return ifaces, nil
}

// GetInterfacesByType returns all interfaces matching the given channel type.
func (db *DB) GetInterfacesByType(channelType string) ([]Interface, error) {
	var ifaces []Interface
	if err := db.Select(&ifaces, "SELECT * FROM interfaces WHERE channel_type = ? ORDER BY id", channelType); err != nil {
		return nil, fmt.Errorf("query interfaces by type: %w", err)
	}
	return ifaces, nil
}

// InsertInterface creates a new interface record.
func (db *DB) InsertInterface(iface *Interface) error {
	_, err := db.Exec(`INSERT INTO interfaces
		(id, channel_type, label, enabled, device_id, device_port, config,
		 ingress_transforms, egress_transforms, ingress_seq, egress_seq)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		iface.ID, iface.ChannelType, iface.Label, iface.Enabled, iface.DeviceID, iface.DevicePort, iface.Config,
		iface.IngressTransforms, iface.EgressTransforms, iface.IngressSeq, iface.EgressSeq)
	if err != nil {
		return fmt.Errorf("insert interface: %w", err)
	}
	return nil
}

// UpdateInterface updates a mutable fields of an existing interface.
func (db *DB) UpdateInterface(iface *Interface) error {
	_, err := db.Exec(`UPDATE interfaces SET
		label=?, enabled=?, device_id=?, device_port=?, config=?,
		ingress_transforms=?, egress_transforms=?, updated_at=datetime('now')
		WHERE id=?`,
		iface.Label, iface.Enabled, iface.DeviceID, iface.DevicePort, iface.Config,
		iface.IngressTransforms, iface.EgressTransforms, iface.ID)
	if err != nil {
		return fmt.Errorf("update interface: %w", err)
	}
	return nil
}

// DeleteInterface removes an interface by ID.
func (db *DB) DeleteInterface(id string) error {
	_, err := db.Exec("DELETE FROM interfaces WHERE id = ?", id)
	return err
}

// IncrementIngressSeq atomically increments ingress_seq and returns the new value.
func (db *DB) IncrementIngressSeq(id string) (int64, error) {
	var seq int64
	err := db.QueryRow(
		"UPDATE interfaces SET ingress_seq = ingress_seq + 1 WHERE id = ? RETURNING ingress_seq", id).Scan(&seq)
	if err != nil {
		return 0, fmt.Errorf("increment ingress seq: %w", err)
	}
	return seq, nil
}

// IncrementEgressSeq atomically increments egress_seq and returns the new value.
func (db *DB) IncrementEgressSeq(id string) (int64, error) {
	var seq int64
	err := db.QueryRow(
		"UPDATE interfaces SET egress_seq = egress_seq + 1 WHERE id = ? RETURNING egress_seq", id).Scan(&seq)
	if err != nil {
		return 0, fmt.Errorf("increment egress seq: %w", err)
	}
	return seq, nil
}

// ---- Object Group CRUD ----

// GetObjectGroup returns a single object group by ID.
func (db *DB) GetObjectGroup(id string) (*ObjectGroup, error) {
	var g ObjectGroup
	if err := db.Get(&g, "SELECT * FROM object_groups WHERE id = ?", id); err != nil {
		return nil, err
	}
	return &g, nil
}

// GetAllObjectGroups returns all object groups.
func (db *DB) GetAllObjectGroups() ([]ObjectGroup, error) {
	var groups []ObjectGroup
	if err := db.Select(&groups, "SELECT * FROM object_groups ORDER BY type, id"); err != nil {
		return nil, fmt.Errorf("query object groups: %w", err)
	}
	return groups, nil
}

// InsertObjectGroup creates a new object group.
func (db *DB) InsertObjectGroup(g *ObjectGroup) error {
	_, err := db.Exec(`INSERT INTO object_groups (id, type, label, members)
		VALUES (?, ?, ?, ?)`,
		g.ID, g.Type, g.Label, g.Members)
	if err != nil {
		return fmt.Errorf("insert object group: %w", err)
	}
	return nil
}

// UpdateObjectGroup updates an existing object group.
func (db *DB) UpdateObjectGroup(g *ObjectGroup) error {
	_, err := db.Exec(`UPDATE object_groups SET type=?, label=?, members=? WHERE id=?`,
		g.Type, g.Label, g.Members, g.ID)
	if err != nil {
		return fmt.Errorf("update object group: %w", err)
	}
	return nil
}

// DeleteObjectGroup removes an object group by ID.
func (db *DB) DeleteObjectGroup(id string) error {
	_, err := db.Exec("DELETE FROM object_groups WHERE id = ?", id)
	return err
}

// ---- Access Rule CRUD ----

// GetAccessRule returns a single access rule by ID.
func (db *DB) GetAccessRule(id int64) (*AccessRule, error) {
	var r AccessRule
	if err := db.Get(&r, "SELECT * FROM access_rules WHERE id = ?", id); err != nil {
		return nil, err
	}
	return &r, nil
}

// GetAccessRules returns rules for a specific interface and direction, ordered by priority.
func (db *DB) GetAccessRules(interfaceID string, direction string) ([]AccessRule, error) {
	var rules []AccessRule
	err := db.Select(&rules,
		"SELECT * FROM access_rules WHERE interface_id = ? AND direction = ? ORDER BY priority ASC, id ASC",
		interfaceID, direction)
	if err != nil {
		return nil, fmt.Errorf("query access rules: %w", err)
	}
	return rules, nil
}

// GetAllAccessRules returns all access rules ordered by priority.
func (db *DB) GetAllAccessRules() ([]AccessRule, error) {
	var rules []AccessRule
	if err := db.Select(&rules, "SELECT * FROM access_rules ORDER BY priority ASC, id ASC"); err != nil {
		return nil, fmt.Errorf("query all access rules: %w", err)
	}
	return rules, nil
}

// InsertAccessRule creates a new access rule and returns its ID.
func (db *DB) InsertAccessRule(r *AccessRule) (int64, error) {
	res, err := db.Exec(`INSERT INTO access_rules
		(interface_id, direction, priority, name, enabled, action, forward_to, filters,
		 filter_node_group, filter_sender_group, filter_portnum_group,
		 schedule_type, schedule_config, forward_options,
		 qos_level, rate_limit_per_min, rate_limit_window)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.InterfaceID, r.Direction, r.Priority, r.Name, r.Enabled, r.Action, r.ForwardTo, r.Filters,
		r.FilterNodeGroup, r.FilterSenderGroup, r.FilterPortnumGroup,
		r.ScheduleType, r.ScheduleConfig, r.ForwardOptions,
		r.QoSLevel, r.RateLimitPerMin, r.RateLimitWindow)
	if err != nil {
		return 0, fmt.Errorf("insert access rule: %w", err)
	}
	return res.LastInsertId()
}

// UpdateAccessRule updates an existing access rule.
func (db *DB) UpdateAccessRule(r *AccessRule) error {
	_, err := db.Exec(`UPDATE access_rules SET
		interface_id=?, direction=?, priority=?, name=?, enabled=?, action=?, forward_to=?, filters=?,
		filter_node_group=?, filter_sender_group=?, filter_portnum_group=?,
		schedule_type=?, schedule_config=?, forward_options=?,
		qos_level=?, rate_limit_per_min=?, rate_limit_window=?, updated_at=datetime('now')
		WHERE id=?`,
		r.InterfaceID, r.Direction, r.Priority, r.Name, r.Enabled, r.Action, r.ForwardTo, r.Filters,
		r.FilterNodeGroup, r.FilterSenderGroup, r.FilterPortnumGroup,
		r.ScheduleType, r.ScheduleConfig, r.ForwardOptions,
		r.QoSLevel, r.RateLimitPerMin, r.RateLimitWindow, r.ID)
	if err != nil {
		return fmt.Errorf("update access rule: %w", err)
	}
	return nil
}

// DeleteAccessRule removes an access rule by ID.
func (db *DB) DeleteAccessRule(id int64) error {
	_, err := db.Exec("DELETE FROM access_rules WHERE id = ?", id)
	return err
}

// SetAccessRuleEnabled sets the enabled flag on an access rule.
func (db *DB) SetAccessRuleEnabled(id int64, enabled bool) error {
	_, err := db.Exec("UPDATE access_rules SET enabled = ?, updated_at = datetime('now') WHERE id = ?", enabled, id)
	if err != nil {
		return fmt.Errorf("set access rule enabled: %w", err)
	}
	return nil
}

// SetAccessRulePriority sets the priority of an access rule (for reordering).
func (db *DB) SetAccessRulePriority(id int64, priority int) error {
	_, err := db.Exec("UPDATE access_rules SET priority = ?, updated_at = datetime('now') WHERE id = ?", priority, id)
	if err != nil {
		return fmt.Errorf("set access rule priority: %w", err)
	}
	return nil
}

// AccessRuleStats holds match statistics for a rule.
type AccessRuleStats struct {
	ID          int64  `json:"id" db:"id"`
	MatchCount  int64  `json:"match_count" db:"match_count"`
	LastMatchAt string `json:"last_match_at" db:"last_match_at"`
}

// GetAccessRuleStats returns match statistics for an access rule.
func (db *DB) GetAccessRuleStats(id int64) (*AccessRuleStats, error) {
	var stats AccessRuleStats
	err := db.Get(&stats, "SELECT id, match_count, COALESCE(last_match_at, '') as last_match_at FROM access_rules WHERE id = ?", id)
	if err != nil {
		return nil, fmt.Errorf("get access rule stats: %w", err)
	}
	return &stats, nil
}

// UpdateAccessRuleMatch increments the match count and sets last_match_at to now.
func (db *DB) UpdateAccessRuleMatch(id int64) error {
	_, err := db.Exec(
		"UPDATE access_rules SET match_count = match_count + 1, last_match_at = datetime('now') WHERE id = ?", id)
	return err
}

// ---- Failover Group CRUD ----

// GetFailoverGroup returns a single failover group by ID.
func (db *DB) GetFailoverGroup(id string) (*FailoverGroup, error) {
	var g FailoverGroup
	if err := db.Get(&g, "SELECT * FROM failover_groups WHERE id = ?", id); err != nil {
		return nil, err
	}
	return &g, nil
}

// GetAllFailoverGroups returns all failover groups.
func (db *DB) GetAllFailoverGroups() ([]FailoverGroup, error) {
	var groups []FailoverGroup
	if err := db.Select(&groups, "SELECT * FROM failover_groups ORDER BY id"); err != nil {
		return nil, fmt.Errorf("query failover groups: %w", err)
	}
	return groups, nil
}

// InsertFailoverGroup creates a new failover group.
func (db *DB) InsertFailoverGroup(g *FailoverGroup) error {
	_, err := db.Exec(`INSERT INTO failover_groups (id, label, mode) VALUES (?, ?, ?)`,
		g.ID, g.Label, g.Mode)
	if err != nil {
		return fmt.Errorf("insert failover group: %w", err)
	}
	return nil
}

// UpdateFailoverGroup updates a failover group's label and mode.
func (db *DB) UpdateFailoverGroup(g *FailoverGroup) error {
	_, err := db.Exec("UPDATE failover_groups SET label = ?, mode = ? WHERE id = ?", g.Label, g.Mode, g.ID)
	if err != nil {
		return fmt.Errorf("update failover group: %w", err)
	}
	return nil
}

// DeleteFailoverMembers removes all members from a failover group.
func (db *DB) DeleteFailoverMembers(groupID string) error {
	_, err := db.Exec("DELETE FROM failover_members WHERE group_id = ?", groupID)
	return err
}

// DeleteFailoverGroup removes a failover group by ID.
func (db *DB) DeleteFailoverGroup(id string) error {
	_, err := db.Exec("DELETE FROM failover_groups WHERE id = ?", id)
	return err
}

// GetFailoverMembers returns all members of a failover group ordered by priority.
func (db *DB) GetFailoverMembers(groupID string) ([]FailoverMember, error) {
	var members []FailoverMember
	if err := db.Select(&members,
		"SELECT * FROM failover_members WHERE group_id = ? ORDER BY priority ASC", groupID); err != nil {
		return nil, fmt.Errorf("query failover members: %w", err)
	}
	return members, nil
}

// InsertFailoverMember adds a member to a failover group.
func (db *DB) InsertFailoverMember(m *FailoverMember) error {
	_, err := db.Exec(`INSERT INTO failover_members (group_id, interface_id, priority)
		VALUES (?, ?, ?)`,
		m.GroupID, m.InterfaceID, m.Priority)
	if err != nil {
		return fmt.Errorf("insert failover member: %w", err)
	}
	return nil
}

// DeleteFailoverMember removes a failover member by ID.
func (db *DB) DeleteFailoverMember(id int64) error {
	_, err := db.Exec("DELETE FROM failover_members WHERE id = ?", id)
	return err
}

// ---- Audit Log ----

// InsertAuditLog persists an audit log entry and returns its ID.
func (db *DB) InsertAuditLog(entry *AuditLogEntry) (int64, error) {
	res, err := db.Exec(`INSERT INTO audit_log
		(timestamp, interface_id, direction, event_type, seq_num, delivery_id, rule_id, detail, prev_hash, hash)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		entry.Timestamp, entry.InterfaceID, entry.Direction, entry.EventType,
		entry.SeqNum, entry.DeliveryID, entry.RuleID, entry.Detail,
		entry.PrevHash, entry.Hash)
	if err != nil {
		return 0, fmt.Errorf("insert audit log: %w", err)
	}
	return res.LastInsertId()
}

// GetAuditLog returns the most recent audit log entries.
func (db *DB) GetAuditLog(limit int) ([]AuditLogEntry, error) {
	var entries []AuditLogEntry
	if err := db.Select(&entries, "SELECT * FROM audit_log ORDER BY id DESC LIMIT ?", limit); err != nil {
		return nil, fmt.Errorf("query audit log: %w", err)
	}
	return entries, nil
}

// GetAuditLogByInterface returns audit log entries for a specific interface.
func (db *DB) GetAuditLogByInterface(interfaceID string, limit int) ([]AuditLogEntry, error) {
	var entries []AuditLogEntry
	if err := db.Select(&entries,
		"SELECT * FROM audit_log WHERE interface_id = ? ORDER BY id DESC LIMIT ?",
		interfaceID, limit); err != nil {
		return nil, fmt.Errorf("query audit log by interface: %w", err)
	}
	return entries, nil
}
