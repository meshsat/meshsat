package database

import (
	"encoding/json"
	"testing"
)

// APRS-R3: v67 relabels an APRS station ack stored as delivered_device
// before the fix (evidence, the route's completion rule, the intent's
// outcome) and leaves a Meshtastic delivered_device alone. [MESHSAT-1584]
func TestMigrationV67_RelabelsAPRSStationAcksOnly(t *testing.T) {
	db := openStaged(t, 66)
	aprsPlan := `[{"route_index":0,"kind":"aprs","adapter_mode":"aprs","completion_rule":"delivered_device","admitted":true},` +
		`{"route_index":1,"kind":"mesh","adapter_mode":"mesh_unicast","completion_rule":"delivered_device","admitted":true}]`
	meshPlan := `[{"route_index":0,"kind":"mesh","adapter_mode":"mesh_unicast","completion_rule":"delivered_device","admitted":true}]`
	for _, in := range []struct{ id, plan string }{{"aprs-intent", aprsPlan}, {"mesh-intent", meshPlan}, {"both-intent", aprsPlan}} {
		if _, err := db.Exec(`INSERT INTO delivery_intents (id, submission_id, payload, payload_sha256, effective_strategy, route_plan, state, outcome)
			VALUES (?, ?, x'00', '', 'ORDERED_FALLBACK', ?, 'closed', 'delivered_device')`, in.id, in.id, in.plan); err != nil {
			t.Fatal(err)
		}
	}
	ev := func(intent, kind, source, event string) {
		t.Helper()
		if _, err := db.Exec(`INSERT INTO delivery_evidence (intent_id, source, event, completes, corr_kind, corr_value, corr_scope, matched)
			VALUES (?, ?, ?, 'delivered_device', ?, 'X', 's', 1)`, intent, source, event, kind); err != nil {
			t.Fatal(err)
		}
	}
	ev("aprs-intent", "aprs_msgid", "station", "ack")
	ev("mesh-intent", "mesh_packet", "radio", "ack")
	ev("both-intent", "mesh_packet", "radio", "ack") // a mesh device ack came too: it stays delivered_device
	ev("both-intent", "aprs_msgid", "station", "ack")
	if _, err := db.Exec(migrations[66]); err != nil {
		t.Fatalf("v67: %v", err)
	}

	outcome := map[string]string{}
	rules := map[string][]string{}
	rows, err := db.Query(`SELECT id, outcome, route_plan FROM delivery_intents`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id, o, plan string
		if err := rows.Scan(&id, &o, &plan); err != nil {
			t.Fatal(err)
		}
		var entries []map[string]any
		if err := json.Unmarshal([]byte(plan), &entries); err != nil {
			t.Fatalf("%s: route plan no longer JSON: %v (%s)", id, err, plan)
		}
		outcome[id] = o
		for _, e := range entries {
			rules[id] = append(rules[id], e["adapter_mode"].(string)+"="+e["completion_rule"].(string))
		}
	}
	rows.Close()
	if outcome["aprs-intent"] != OutcomeAckedStation || outcome["mesh-intent"] != OutcomeDeliveredDevice || outcome["both-intent"] != OutcomeDeliveredDevice {
		t.Errorf("outcomes %v; want aprs-intent acked_station, the others delivered_device", outcome)
	}
	for _, id := range []string{"aprs-intent", "both-intent"} {
		if got := rules[id]; len(got) != 2 || got[0] != "aprs=acked_station" || got[1] != "mesh_unicast=delivered_device" {
			t.Errorf("%s rules %v; want the APRS route relabelled, the mesh route and the order kept", id, got)
		}
	}
	if got := rules["mesh-intent"]; len(got) != 1 || got[0] != "mesh_unicast=delivered_device" {
		t.Errorf("mesh-intent rules %v", got)
	}
	var aprsEv, meshEv int
	db.QueryRow(`SELECT COUNT(*) FROM delivery_evidence WHERE corr_kind = 'aprs_msgid' AND completes = 'acked_station'`).Scan(&aprsEv)
	db.QueryRow(`SELECT COUNT(*) FROM delivery_evidence WHERE corr_kind = 'mesh_packet' AND completes = 'delivered_device'`).Scan(&meshEv)
	if aprsEv != 2 || meshEv != 2 {
		t.Errorf("evidence: %d APRS relabelled, %d mesh kept; want 2 and 2", aprsEv, meshEv)
	}
}
