package api

import (
	"bytes"
	"context"
	"encoding/csv"
	"io"
	"net/http"
	"strings"

	"meshsat/internal/directory"
)

// Directory import + export endpoints. These operate against the
// unified internal/directory.SQLStore (v44+), not the legacy v23
// contacts tables. [MESHSAT-541]
//
// The vCard format is RFC 6350 with X-MESHSAT-* extensions for the
// bearer kinds not covered by standard TEL / EMAIL (Meshtastic,
// APRS, Iridium, TAK, Reticulum, ZigBee, BLE, Webhook, MQTT, plus
// Team / Role / SIDC / TrustLevel metadata).
//
// CSV uses a minimal operator-friendly column layout:
//   display_name, sms, meshtastic, aprs, email, team, role
// Unknown columns are ignored; missing columns are treated as
// empty. One contact per row. Suitable for quick hand-maintained
// roster spreadsheets.

// @Summary Import contacts from vCard 4.0
// @Description Uploads a .vcf file (Content-Type: text/vcard) and
// @Description creates directory_contacts + directory_addresses rows
// @Description for each record. X-MESHSAT-* extensions map to the
// @Description bearer kinds not covered by standard vCard. Contacts
// @Description that fail validation are skipped; the response lists
// @Description how many imported vs errored.
// @Tags directory
// @Accept text/vcard
// @Produce json
// @Param body body string true "vCard 4.0 text (one or more BEGIN/END records)"
// @Success 200 {object} map[string]any
// @Failure 400 {object} map[string]string
// @Failure 500 {object} map[string]string
// @Router /api/directory/import/vcard [post]
func (s *Server) handleImportVCard(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 10<<20)) // 10 MB cap
	if err != nil {
		writeError(w, http.StatusBadRequest, "read body: "+err.Error())
		return
	}
	contacts, err := directory.ParseVCards(bytes.NewReader(body))
	if err != nil {
		writeError(w, http.StatusBadRequest, "parse vcard: "+err.Error())
		return
	}
	store := directory.NewSQLStore(s.db.DB)
	imported, errored := applyImport(r.Context(), store, contacts)
	writeJSON(w, http.StatusOK, map[string]any{
		"parsed":   len(contacts),
		"imported": imported,
		"errors":   errored,
	})
}

// @Summary Import contacts from CSV
// @Description Uploads a CSV file (Content-Type: text/csv) with the
// @Description column header: `display_name,sms,meshtastic,aprs,email,team,role`.
// @Description Unknown headers are ignored; missing columns are
// @Description treated as empty. One contact per data row.
// @Tags directory
// @Accept text/csv
// @Produce json
// @Param body body string true "CSV with header row"
// @Success 200 {object} map[string]any
// @Failure 400 {object} map[string]string
// @Router /api/directory/import/csv [post]
func (s *Server) handleImportCSV(w http.ResponseWriter, r *http.Request) {
	reader := csv.NewReader(io.LimitReader(r.Body, 10<<20))
	reader.FieldsPerRecord = -1 // tolerate ragged rows
	records, err := reader.ReadAll()
	if err != nil {
		writeError(w, http.StatusBadRequest, "parse csv: "+err.Error())
		return
	}
	if len(records) < 2 {
		writeError(w, http.StatusBadRequest, "csv must have a header row plus at least one record")
		return
	}
	headers := records[0]
	colIdx := map[string]int{}
	for i, h := range headers {
		colIdx[strings.ToLower(strings.TrimSpace(h))] = i
	}
	contacts := make([]directory.Contact, 0, len(records)-1)
	for _, row := range records[1:] {
		cell := func(key string) string {
			i, ok := colIdx[key]
			if !ok || i >= len(row) {
				return ""
			}
			return strings.TrimSpace(row[i])
		}
		name := cell("display_name")
		if name == "" {
			continue // skip blank rows
		}
		c := directory.Contact{
			DisplayName: name,
			Team:        cell("team"),
			Role:        cell("role"),
			Origin:      directory.OriginImported,
		}
		csvAppend := func(k directory.Kind, col string) {
			v := cell(col)
			if v == "" {
				return
			}
			c.Addresses = append(c.Addresses, directory.Address{
				Kind:        k,
				Value:       v,
				PrimaryRank: 0,
			})
		}
		csvAppend(directory.KindSMS, "sms")
		csvAppend(directory.KindMeshtastic, "meshtastic")
		csvAppend(directory.KindAPRS, "aprs")
		csvAppend(directory.KindEmail, "email")
		contacts = append(contacts, c)
	}
	store := directory.NewSQLStore(s.db.DB)
	imported, errored := applyImport(r.Context(), store, contacts)
	writeJSON(w, http.StatusOK, map[string]any{
		"parsed":   len(contacts),
		"imported": imported,
		"errors":   errored,
	})
}

// applyImport pushes a batch of parsed contacts through the Store.
// Conflict (UNIQUE(kind,value)) is treated as a soft skip — we count
// the row as errored but the import continues. Hard failures halt.
func applyImport(ctx context.Context, store *directory.SQLStore, contacts []directory.Contact) (imported int, errored int) {
	for i := range contacts {
		c := &contacts[i]
		if c.Origin == "" {
			c.Origin = directory.OriginImported
		}
		if err := store.CreateContact(ctx, c); err != nil {
			errored++
			continue
		}
		// Addresses are a separate table; transfer each one.
		addressesErrored := 0
		for j := range c.Addresses {
			a := &c.Addresses[j]
			a.ContactID = c.ID
			if err := store.AddAddress(ctx, a); err != nil {
				addressesErrored++
				// Conflict on (kind,value) is soft — a previous import
				// already owns it. Continue with the next address.
			}
		}
		if addressesErrored == len(c.Addresses) && len(c.Addresses) > 0 {
			// Every address failed — roll back the contact so we
			// don't leave an empty shell.
			_ = store.DeleteContact(ctx, c.ID)
			errored++
			continue
		}
		imported++
	}
	return imported, errored
}
