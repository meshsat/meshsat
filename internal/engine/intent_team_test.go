package engine

import "testing"

// TestRequestHashOfAContactSendIsUnchanged: the hash a contact send's
// Idempotency-Key is checked against does not change with the team field,
// so a key stored before a team send existed still matches its request;
// a team send hashes its group. The values are computed outside Go from the
// canonical JSON (sorted keys). [MESHSAT-1584]
func TestRequestHashOfAContactSendIsUnchanged(t *testing.T) {
	contact := IntentRequest{ContactID: "c-1", Body: []byte("hello"), Precedence: "Routine"}
	if got := contact.fingerprint(); got != "83fcd6be78fa2d72a559b74914d32a9681269bd7f45f9af7330239f77b7cd1e3" {
		t.Errorf("contact send hash %s changed", got)
	}
	team := IntentRequest{GroupID: "team-1", Body: []byte("hello"), Precedence: "Routine"}
	if got := team.fingerprint(); got != "48976d403f612d9c6858fef822a17ca62430a5c9740f7be7c6dbd88cd2f100c5" {
		t.Errorf("team send hash %s", got)
	}
}
