package hubreporter

import (
	"errors"
	"testing"
	"time"
)

// The r30 review's Appendix B, adapted to the corrected interface as the
// review asks: the store now answers its failures as errors, and a stamp is
// only handed out once the floor was read and the stamp stored. The faults
// and the assertions are the review's; a stamp the reporter withholds (an
// error) is never published, so only stamps handed out are checked.
type reviewR30FaultStamps struct {
	last                  time.Time
	failLoad, failPersist bool
}

func (s *reviewR30FaultStamps) LastBirthStamp() (time.Time, error) {
	if s.failLoad {
		return time.Time{}, errors.New("injected stamp read failure")
	}
	return s.last, nil
}

func (s *reviewR30FaultStamps) SaveBirthStamp(at time.Time) error {
	if s.failPersist {
		return errors.New("injected stamp save failure")
	}
	s.last = at
	return nil
}

func TestReviewR30BirthOrderSurvivesStampStorageFailures(t *testing.T) {
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	previous := now.Add(time.Hour)
	t.Run("failed initial load", func(t *testing.T) {
		s := &reviewR30FaultStamps{last: previous, failLoad: true}
		r := &HubReporter{cfg: ReporterConfig{BirthStamps: s}}
		first, err := r.birthStamp(now)
		if err == nil {
			t.Errorf("a stamp was handed out without the floor: %s", first.Format(time.RFC3339Nano))
		}
		if !s.last.Equal(previous) {
			t.Errorf("the durable floor moved while it could not be read: %s", s.last.Format(time.RFC3339Nano))
		}
		s.failLoad = false
		second, err := r.birthStamp(now.Add(time.Second))
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("previous=%s first=withheld after_read_recovery=%s durable=%s", previous.Format(time.RFC3339Nano), second.Format(time.RFC3339Nano), s.last.Format(time.RFC3339Nano))
		if !second.After(previous) || !s.last.Equal(second) {
			t.Error("failed initial floor read allowed older birth stamps and erased the durable floor")
		}
	})
	t.Run("failed saves then restart", func(t *testing.T) {
		s := &reviewR30FaultStamps{last: previous}
		r := &HubReporter{cfg: ReporterConfig{BirthStamps: s}}
		first, err := r.birthStamp(now)
		if err != nil {
			t.Fatal(err)
		}
		s.failPersist = true
		if withheld, err := r.birthStamp(now.Add(time.Second)); err == nil {
			t.Errorf("a stamp that could not be stored was handed out: %s", withheld.Format(time.RFC3339Nano))
		}
		s.failPersist = false
		latestBeforeRestart, err := r.birthStamp(now.Add(time.Second))
		if err != nil {
			t.Fatal(err)
		}
		if !latestBeforeRestart.After(first) {
			t.Fatal("premise: in-process stamps did not advance")
		}
		restarted := &HubReporter{cfg: ReporterConfig{BirthStamps: s}}
		afterRestart, err := restarted.birthStamp(now.Add(2 * time.Second))
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("first=%s latest_before_restart=%s after_restart=%s", first.Format(time.RFC3339Nano), latestBeforeRestart.Format(time.RFC3339Nano), afterRestart.Format(time.RFC3339Nano))
		if !afterRestart.After(latestBeforeRestart) {
			t.Error("failed floor persistence allowed a restarted reporter to reuse an older stamp")
		}
	})
}
