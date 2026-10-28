package database

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// TAK chat threads: who last wrote to a mesh node from TAK or the Hub map, so
// the node's answer goes back to them (gateway.TAKChat). [MESHSAT-1705]

// takChatThreadKeep is how long a thread row is kept at all; it is read for a
// day (gateway.TAKChatThreadLife).
const takChatThreadKeep = 7 * 24 * time.Hour

// OpenTAKChatThread records that peer wrote to node at at: node's next text to
// this Bridge goes back to peer. The latest writer replaces any earlier one.
func (db *DB) OpenTAKChatThread(node uint32, peerUID, peerCallsign string, at time.Time) error {
	at = at.UTC()
	if _, err := db.Exec(`INSERT INTO tak_chat_threads (node, peer_uid, peer_callsign, last_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(node) DO UPDATE SET peer_uid = excluded.peer_uid,
			peer_callsign = excluded.peer_callsign, bearer = '', message_id = '', reply_here = 0,
			last_at = excluded.last_at`,
		takChatNode(node), peerUID, peerCallsign, at.Format(time.RFC3339Nano)); err != nil {
		return fmt.Errorf("open tak chat thread: %w", err)
	}
	if _, err := db.Exec(`DELETE FROM tak_chat_threads WHERE last_at < ?`,
		at.Add(-takChatThreadKeep).Format(time.RFC3339Nano)); err != nil {
		return fmt.Errorf("prune tak chat threads: %w", err)
	}
	return nil
}

// TAKChatThread returns who last wrote to node, if that was at since or later.
func (db *DB) TAKChatThread(node uint32, since time.Time) (peerUID, peerCallsign string, ok bool, err error) {
	var lastAt string
	err = db.QueryRow(`SELECT peer_uid, peer_callsign, last_at FROM tak_chat_threads WHERE node = ?`,
		takChatNode(node)).Scan(&peerUID, &peerCallsign, &lastAt)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", false, nil
	}
	if err != nil {
		return "", "", false, fmt.Errorf("read tak chat thread: %w", err)
	}
	at, err := time.Parse(time.RFC3339Nano, lastAt)
	if err != nil {
		return "", "", false, fmt.Errorf("read tak chat thread: %w", err)
	}
	if at.Before(since) {
		return "", "", false, nil
	}
	return peerUID, peerCallsign, true, nil
}

// OpenTAKChatThreadVia is OpenTAKChatThread that also records how peer
// wrote: the bearer and message id of the frame the chat came as ("" for
// MQTT or a TAK server) and whether the Hub asked for the answer on that
// bearer, so the node's answer goes back the same way
// (gateway.TAKChatThreadsVia). [MESHSAT-1744]
func (db *DB) OpenTAKChatThreadVia(node uint32, peerUID, peerCallsign, bearer, messageID string, replyHere bool, at time.Time) error {
	at = at.UTC()
	if _, err := db.Exec(`INSERT INTO tak_chat_threads (node, peer_uid, peer_callsign, bearer, message_id, reply_here, last_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(node) DO UPDATE SET peer_uid = excluded.peer_uid,
			peer_callsign = excluded.peer_callsign, bearer = excluded.bearer, message_id = excluded.message_id,
			reply_here = excluded.reply_here, last_at = excluded.last_at`,
		takChatNode(node), peerUID, peerCallsign, bearer, messageID, replyHere, at.Format(time.RFC3339Nano)); err != nil {
		return fmt.Errorf("open tak chat thread: %w", err)
	}
	if _, err := db.Exec(`DELETE FROM tak_chat_threads WHERE last_at < ?`,
		at.Add(-takChatThreadKeep).Format(time.RFC3339Nano)); err != nil {
		return fmt.Errorf("prune tak chat threads: %w", err)
	}
	return nil
}

// TAKChatThreadVia is TAKChatThread with how the last writer wrote.
func (db *DB) TAKChatThreadVia(node uint32, since time.Time) (peerUID, peerCallsign, bearer, messageID string, replyHere bool, ok bool, err error) {
	var lastAt string
	err = db.QueryRow(`SELECT peer_uid, peer_callsign, bearer, message_id, reply_here, last_at FROM tak_chat_threads WHERE node = ?`,
		takChatNode(node)).Scan(&peerUID, &peerCallsign, &bearer, &messageID, &replyHere, &lastAt)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", "", "", false, false, nil
	}
	if err != nil {
		return "", "", "", "", false, false, fmt.Errorf("read tak chat thread: %w", err)
	}
	at, err := time.Parse(time.RFC3339Nano, lastAt)
	if err != nil {
		return "", "", "", "", false, false, fmt.Errorf("read tak chat thread: %w", err)
	}
	if at.Before(since) {
		return "", "", "", "", false, false, nil
	}
	return peerUID, peerCallsign, bearer, messageID, replyHere, true, nil
}

func takChatNode(node uint32) string { return fmt.Sprintf("!%08x", node) }
