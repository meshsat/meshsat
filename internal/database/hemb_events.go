package database

import "fmt"

// HeMBEvent represents a persisted HeMB observability event.
type HeMBEvent struct {
	ID           int64  `db:"id" json:"id"`
	Timestamp    string `db:"ts" json:"ts"`
	EventType    string `db:"event_type" json:"event_type"`
	StreamID     *int   `db:"stream_id" json:"stream_id,omitempty"`
	GenerationID *int   `db:"generation_id" json:"generation_id,omitempty"`
	BearerIdx    *int   `db:"bearer_idx" json:"bearer_idx,omitempty"`
	Payload      string `db:"payload" json:"payload"`
}

// InsertHeMBEvent persists a HeMB event.
func (db *DB) InsertHeMBEvent(eventType string, streamID, generationID, bearerIdx *int, payload string) error {
	_, err := db.Exec(
		`INSERT INTO hemb_events (event_type, stream_id, generation_id, bearer_idx, payload) VALUES (?, ?, ?, ?, ?)`,
		eventType, streamID, generationID, bearerIdx, payload,
	)
	return err
}

// GetHeMBEvents returns recent HeMB events, optionally filtered by stream/generation.
func (db *DB) GetHeMBEvents(streamID, generationID *int, limit int) ([]HeMBEvent, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	var events []HeMBEvent
	var err error

	if streamID != nil && generationID != nil {
		err = db.Select(&events,
			`SELECT * FROM hemb_events WHERE stream_id = ? AND generation_id = ? ORDER BY ts DESC LIMIT ?`,
			*streamID, *generationID, limit)
	} else if streamID != nil {
		err = db.Select(&events,
			`SELECT * FROM hemb_events WHERE stream_id = ? ORDER BY ts DESC LIMIT ?`,
			*streamID, limit)
	} else {
		err = db.Select(&events,
			`SELECT * FROM hemb_events ORDER BY ts DESC LIMIT ?`, limit)
	}
	if err != nil {
		return nil, fmt.Errorf("query hemb events: %w", err)
	}
	return events, nil
}

// ReapHeMBEvents removes events older than the given number of days.
func (db *DB) ReapHeMBEvents(retentionDays int) (int64, error) {
	result, err := db.Exec(
		`DELETE FROM hemb_events WHERE ts < datetime('now', ? || ' days')`,
		fmt.Sprintf("-%d", retentionDays),
	)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}
