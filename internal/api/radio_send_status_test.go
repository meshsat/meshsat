package api

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"meshsat/internal/transport"
)

// A send the radio would have dropped (its interval for the port has not
// passed) answers 429 and says when the next can go; any other failure 500.
// [MESHSAT-1682]
func TestRadioSendStatus(t *testing.T) {
	busy := fmt.Errorf("%w: the next can go in 7.2s", transport.ErrRadioPortBusy)
	if got := radioSendStatus(busy); got != http.StatusTooManyRequests {
		t.Fatalf("busy: %d, want 429", got)
	}
	if got := radioSendStatus(errors.New("not connected")); got != http.StatusInternalServerError {
		t.Fatalf("other: %d, want 500", got)
	}
}
