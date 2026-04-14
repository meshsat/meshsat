package database

import (
	"fmt"
)

// Contact represents a unified contact entity (person, hub, or device).
type Contact struct {
	ID          int64            `json:"id"`
	DisplayName string           `json:"display_name"`
	Notes       string           `json:"notes"`
	Addresses   []ContactAddress `json:"addresses,omitempty"`
	CreatedAt   string           `json:"created_at"`
	UpdatedAt   string           `json:"updated_at"`
}

// ContactAddress represents one transport address for a contact.
type ContactAddress struct {
	ID            int64  `json:"id"`
	ContactID     int64  `json:"contact_id"`
	Type          string `json:"type"`    // mesh, sms, webhook, mqtt, iridium, zigbee
	Address       string `json:"address"` // phone number, node ID, URL, topic, etc.
	Label         string `json:"label"`
	EncryptionKey string `json:"encryption_key,omitempty"`
	IsPrimary     bool   `json:"is_primary"`
	AutoFwd       bool   `json:"auto_fwd"`
	CreatedAt     string `json:"created_at"`
}

// UnifiedMessage represents a message from any transport in a unified timeline.
type UnifiedMessage struct {
	ID        int64  `json:"id"`
	Transport string `json:"transport"` // sms, mesh, iridium, webhook, etc.
	Direction string `json:"direction"` // rx, tx
	Address   string `json:"address"`   // the address this message was sent to/from
	Text      string `json:"text"`
	Status    string `json:"status,omitempty"`
	Timestamp int64  `json:"timestamp"` // unix seconds
	RawJSON   string `json:"raw,omitempty"`
}

// GetContacts returns all contacts with their addresses.
func (db *DB) GetContacts() ([]Contact, error) {
	rows, err := db.Query("SELECT id, display_name, notes, created_at, updated_at FROM contacts ORDER BY display_name")
	if err != nil {
		return nil, fmt.Errorf("get contacts: %w", err)
	}
	defer rows.Close()

	var contacts []Contact
	for rows.Next() {
		var c Contact
		if err := rows.Scan(&c.ID, &c.DisplayName, &c.Notes, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan contact: %w", err)
		}
		contacts = append(contacts, c)
	}

	// Load addresses for all contacts
	for i := range contacts {
		addrs, err := db.GetContactAddresses(contacts[i].ID)
		if err != nil {
			return nil, err
		}
		contacts[i].Addresses = addrs
	}
	return contacts, nil
}

// GetContact returns a single contact with addresses.
func (db *DB) GetContact(id int64) (*Contact, error) {
	var c Contact
	err := db.QueryRow("SELECT id, display_name, notes, created_at, updated_at FROM contacts WHERE id = ?", id).
		Scan(&c.ID, &c.DisplayName, &c.Notes, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("get contact %d: %w", id, err)
	}
	addrs, err := db.GetContactAddresses(id)
	if err != nil {
		return nil, err
	}
	c.Addresses = addrs
	return &c, nil
}

// CreateContact creates a new contact and returns its ID.
func (db *DB) CreateContact(name, notes string) (int64, error) {
	res, err := db.Exec("INSERT INTO contacts (display_name, notes) VALUES (?, ?)", name, notes)
	if err != nil {
		return 0, fmt.Errorf("create contact: %w", err)
	}
	return res.LastInsertId()
}

// UpdateContact updates a contact's name and notes.
func (db *DB) UpdateContact(id int64, name, notes string) error {
	_, err := db.Exec("UPDATE contacts SET display_name = ?, notes = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?",
		name, notes, id)
	return err
}

// DeleteContact removes a contact and all its addresses (CASCADE).
func (db *DB) DeleteContact(id int64) error {
	// SQLite foreign_keys must be enabled for CASCADE; do explicit delete as safety net
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec("DELETE FROM contact_addresses WHERE contact_id = ?", id); err != nil {
		return fmt.Errorf("delete addresses for contact %d: %w", id, err)
	}
	if _, err := tx.Exec("DELETE FROM contacts WHERE id = ?", id); err != nil {
		return fmt.Errorf("delete contact %d: %w", id, err)
	}
	return tx.Commit()
}

// GetContactAddresses returns all addresses for a contact.
func (db *DB) GetContactAddresses(contactID int64) ([]ContactAddress, error) {
	rows, err := db.Query(
		"SELECT id, contact_id, type, address, label, encryption_key, is_primary, auto_fwd, created_at FROM contact_addresses WHERE contact_id = ? ORDER BY is_primary DESC, type, address",
		contactID)
	if err != nil {
		return nil, fmt.Errorf("get addresses for contact %d: %w", contactID, err)
	}
	defer rows.Close()

	var addrs []ContactAddress
	for rows.Next() {
		var a ContactAddress
		if err := rows.Scan(&a.ID, &a.ContactID, &a.Type, &a.Address, &a.Label, &a.EncryptionKey, &a.IsPrimary, &a.AutoFwd, &a.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan address: %w", err)
		}
		addrs = append(addrs, a)
	}
	return addrs, nil
}

// AddContactAddress adds a transport address to a contact.
func (db *DB) AddContactAddress(contactID int64, addrType, address, label, encKey string, isPrimary, autoFwd bool) (int64, error) {
	res, err := db.Exec(
		"INSERT INTO contact_addresses (contact_id, type, address, label, encryption_key, is_primary, auto_fwd) VALUES (?, ?, ?, ?, ?, ?, ?)",
		contactID, addrType, address, label, encKey, isPrimary, autoFwd)
	if err != nil {
		return 0, fmt.Errorf("add address: %w", err)
	}
	return res.LastInsertId()
}

// UpdateContactAddress updates a transport address.
func (db *DB) UpdateContactAddress(id int64, addrType, address, label, encKey string, isPrimary, autoFwd bool) error {
	_, err := db.Exec(
		"UPDATE contact_addresses SET type = ?, address = ?, label = ?, encryption_key = ?, is_primary = ?, auto_fwd = ? WHERE id = ?",
		addrType, address, label, encKey, isPrimary, autoFwd, id)
	return err
}

// DeleteContactAddress removes a transport address.
func (db *DB) DeleteContactAddress(id int64) error {
	_, err := db.Exec("DELETE FROM contact_addresses WHERE id = ?", id)
	return err
}

// ResolveContact looks up a contact by transport type and address.
func (db *DB) ResolveContact(addrType, address string) (*Contact, error) {
	var contactID int64
	err := db.QueryRow("SELECT contact_id FROM contact_addresses WHERE type = ? AND address = ?", addrType, address).Scan(&contactID)
	if err != nil {
		return nil, err
	}
	return db.GetContact(contactID)
}

// GetUnifiedConversation returns all messages across all addresses for a contact, sorted by time.
func (db *DB) GetUnifiedConversation(contactID int64, limit int) ([]UnifiedMessage, error) {
	if limit <= 0 {
		limit = 100
	}

	// Get all addresses for this contact
	addrs, err := db.GetContactAddresses(contactID)
	if err != nil {
		return nil, err
	}

	var messages []UnifiedMessage

	for _, addr := range addrs {
		switch addr.Type {
		case "sms":
			msgs, err := db.getConvSMS(addr.Address, limit)
			if err != nil {
				return nil, err
			}
			messages = append(messages, msgs...)

		case "mesh":
			msgs, err := db.getConvMesh(addr.Address, limit)
			if err != nil {
				return nil, err
			}
			messages = append(messages, msgs...)
		}
		// webhook, mqtt, iridium, etc. can be added as they get message history tables
	}

	// Sort by timestamp descending, then limit
	sortUnifiedMessages(messages)
	if len(messages) > limit {
		messages = messages[:limit]
	}
	return messages, nil
}

func (db *DB) getConvSMS(phone string, limit int) ([]UnifiedMessage, error) {
	rows, err := db.Query(
		"SELECT id, direction, phone, text, status, timestamp FROM sms_messages WHERE phone = ? ORDER BY timestamp DESC LIMIT ?",
		phone, limit)
	if err != nil {
		return nil, fmt.Errorf("conv sms: %w", err)
	}
	defer rows.Close()

	var msgs []UnifiedMessage
	for rows.Next() {
		var m UnifiedMessage
		var ts int64
		if err := rows.Scan(&m.ID, &m.Direction, &m.Address, &m.Text, &m.Status, &ts); err != nil {
			return nil, err
		}
		m.Transport = "sms"
		m.Timestamp = ts
		msgs = append(msgs, m)
	}
	return msgs, nil
}

func (db *DB) getConvMesh(nodeID string, limit int) ([]UnifiedMessage, error) {
	// Normalize: mesh addresses may be stored as "!abcd1234" — match from_node or to_node
	rows, err := db.Query(
		`SELECT id, direction, from_node, decoded_text, '', CAST(COALESCE(rx_time, strftime('%s', created_at)) AS INTEGER)
		 FROM messages WHERE (from_node = ? OR to_node = ?) AND decoded_text != '' ORDER BY created_at DESC LIMIT ?`,
		nodeID, nodeID, limit)
	if err != nil {
		return nil, fmt.Errorf("conv mesh: %w", err)
	}
	defer rows.Close()

	var msgs []UnifiedMessage
	for rows.Next() {
		var m UnifiedMessage
		if err := rows.Scan(&m.ID, &m.Direction, &m.Address, &m.Text, &m.Status, &m.Timestamp); err != nil {
			return nil, err
		}
		m.Transport = "mesh"
		msgs = append(msgs, m)
	}
	return msgs, nil
}

// sortUnifiedMessages sorts by timestamp descending (newest first).
func sortUnifiedMessages(msgs []UnifiedMessage) {
	for i := 1; i < len(msgs); i++ {
		for j := i; j > 0 && msgs[j].Timestamp > msgs[j-1].Timestamp; j-- {
			msgs[j], msgs[j-1] = msgs[j-1], msgs[j]
		}
	}
}

// LookupContactByAddress returns the contact name for a given address type + address.
// Returns empty string if not found. Used by conversation views to resolve names.
func (db *DB) LookupContactByAddress(addrType, address string) string {
	var name string
	_ = db.QueryRow(
		"SELECT c.display_name FROM contacts c JOIN contact_addresses ca ON c.id = ca.contact_id WHERE ca.type = ? AND ca.address = ?",
		addrType, address).Scan(&name)
	return name
}

// GetContactsWithAddressType returns contacts that have at least one address of the given type.
func (db *DB) GetContactsWithAddressType(addrType string) ([]Contact, error) {
	rows, err := db.Query(
		`SELECT DISTINCT c.id, c.display_name, c.notes, c.created_at, c.updated_at
		 FROM contacts c JOIN contact_addresses ca ON c.id = ca.contact_id
		 WHERE ca.type = ? ORDER BY c.display_name`, addrType)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var contacts []Contact
	for rows.Next() {
		var c Contact
		if err := rows.Scan(&c.ID, &c.DisplayName, &c.Notes, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, err
		}
		contacts = append(contacts, c)
	}
	for i := range contacts {
		addrs, err := db.GetContactAddresses(contacts[i].ID)
		if err != nil {
			return nil, err
		}
		contacts[i].Addresses = addrs
	}
	return contacts, nil
}

// SyncSMSContactToUnified ensures an SMS contact exists in the unified contacts table.
// Used as a bridge during transition — old code creating sms_contacts also syncs to unified.
func (db *DB) SyncSMSContactToUnified(name, phone, notes string, autoFwd bool) error {
	// Check if address already exists
	var existing int
	err := db.QueryRow("SELECT COUNT(*) FROM contact_addresses WHERE type = 'sms' AND address = ?", phone).Scan(&existing)
	if err != nil {
		return err
	}
	if existing > 0 {
		// Update the contact name if it changed
		var contactID int64
		_ = db.QueryRow("SELECT contact_id FROM contact_addresses WHERE type = 'sms' AND address = ?", phone).Scan(&contactID)
		if contactID > 0 {
			_, _ = db.Exec("UPDATE contacts SET display_name = ?, notes = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?", name, notes, contactID)
			_, _ = db.Exec("UPDATE contact_addresses SET auto_fwd = ? WHERE type = 'sms' AND address = ?", autoFwd, phone)
		}
		return nil
	}

	// Create new unified contact
	cID, err := db.CreateContact(name, notes)
	if err != nil {
		return err
	}
	_, err = db.AddContactAddress(cID, "sms", phone, "Phone", "", true, autoFwd)
	return err
}

// MergeAddressTypes returns a comma-separated summary of transport types for a contact.
func (c *Contact) AddressTypes() []string {
	seen := map[string]bool{}
	var types []string
	for _, a := range c.Addresses {
		if !seen[a.Type] {
			seen[a.Type] = true
			types = append(types, a.Type)
		}
	}
	return types
}

// PrimaryAddress returns the primary address of a given type, or the first one.
func (c *Contact) PrimaryAddress(addrType string) *ContactAddress {
	var first *ContactAddress
	for i, a := range c.Addresses {
		if a.Type != addrType {
			continue
		}
		if a.IsPrimary {
			return &c.Addresses[i]
		}
		if first == nil {
			first = &c.Addresses[i]
		}
	}
	return first
}

// AllPhones returns all SMS addresses for this contact.
func (c *Contact) AllPhones() []string {
	var phones []string
	for _, a := range c.Addresses {
		if a.Type == "sms" {
			phones = append(phones, a.Address)
		}
	}
	return phones
}

// AllMeshNodes returns all mesh node IDs for this contact.
func (c *Contact) AllMeshNodes() []string {
	var nodes []string
	for _, a := range c.Addresses {
		if a.Type == "mesh" {
			nodes = append(nodes, a.Address)
		}
	}
	return nodes
}

// HasEncryptedAddress returns true if any address has an encryption key set.
func (c *Contact) HasEncryptedAddress() bool {
	for _, a := range c.Addresses {
		if a.EncryptionKey != "" {
			return true
		}
	}
	return false
}
