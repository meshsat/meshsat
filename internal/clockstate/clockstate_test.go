package clockstate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParse(t *testing.T) {
	tests := []struct {
		name        string
		in          string
		wantTrusted bool
		wantSource  string
		wantBoot    string
	}{
		{
			name:        "guard could not establish the time",
			in:          "trusted=no\nsource=none\nboot_id=abc\nchecked_at=2026-09-12T08:00:00Z\n",
			wantTrusted: false,
			wantSource:  "none",
			wantBoot:    "abc",
		},
		{
			name:        "clock came from the cellular network",
			in:          "trusted=yes\nsource=cellular\nboot_id=abc\n",
			wantTrusted: true,
			wantSource:  "cellular",
			wantBoot:    "abc",
		},
		{
			name:        "comments and blank lines are ignored",
			in:          "# written by the guard\n\ntrusted=no\nsource=none\n",
			wantTrusted: false,
			wantSource:  "none",
		},
		{
			name:        "quoted values",
			in:          "trusted=\"no\"\nsource=\"none\"\n",
			wantTrusted: false,
			wantSource:  "none",
		},
		{
			name:        "unparseable file keeps trust",
			in:          "this is not a state file at all\n",
			wantTrusted: true,
			wantSource:  SourceUnknown,
		},
		{
			name:        "an unrecognised trusted value keeps trust",
			in:          "trusted=maybe\nsource=weird\n",
			wantTrusted: true,
			wantSource:  "weird",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Parse(strings.NewReader(tt.in))
			if got.Trusted != tt.wantTrusted {
				t.Errorf("Trusted = %v, want %v", got.Trusted, tt.wantTrusted)
			}
			if got.Source != tt.wantSource {
				t.Errorf("Source = %q, want %q", got.Source, tt.wantSource)
			}
			if tt.wantBoot != "" && got.BootID != tt.wantBoot {
				t.Errorf("BootID = %q, want %q", got.BootID, tt.wantBoot)
			}
		})
	}
}

func TestParseCheckedAt(t *testing.T) {
	got := Parse(strings.NewReader("trusted=no\nchecked_at=2026-09-12T08:03:49Z\n"))
	want := time.Date(2026, 9, 12, 8, 3, 49, 0, time.UTC)
	if !got.CheckedAt.Equal(want) {
		t.Errorf("CheckedAt = %v, want %v", got.CheckedAt, want)
	}
}

func newTestProvider(t *testing.T, body, bootID string) *Provider {
	t.Helper()
	path := filepath.Join(t.TempDir(), "clock-state")
	if body != "" {
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	p := NewProvider(path)
	p.bootID = func() string { return bootID }
	return p
}

func TestProviderFailsOpen(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		bootID  string
		want    bool
		wantSrc string
	}{
		{
			name:    "no state file at all (no guard installed)",
			body:    "",
			bootID:  "boot-1",
			want:    true,
			wantSrc: SourceUnknown,
		},
		{
			name:    "verdict from an earlier boot is ignored",
			body:    "trusted=no\nsource=none\nboot_id=boot-0\n",
			bootID:  "boot-1",
			want:    true,
			wantSrc: SourceStale,
		},
		{
			name:    "verdict from this boot is honoured",
			body:    "trusted=no\nsource=none\nboot_id=boot-1\n",
			bootID:  "boot-1",
			want:    false,
			wantSrc: "none",
		},
		{
			name:    "no boot id in the file is honoured as current",
			body:    "trusted=no\nsource=none\n",
			bootID:  "boot-1",
			want:    false,
			wantSrc: "none",
		},
		{
			name:    "boot id unreadable in this environment",
			body:    "trusted=no\nsource=none\nboot_id=boot-0\n",
			bootID:  "",
			want:    false,
			wantSrc: "none",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := newTestProvider(t, tt.body, tt.bootID)
			if got := p.Trusted(); got != tt.want {
				t.Errorf("Trusted() = %v, want %v", got, tt.want)
			}
			if got := p.State().Source; got != tt.wantSrc {
				t.Errorf("Source = %q, want %q", got, tt.wantSrc)
			}
		})
	}
}

func TestProviderEmptyPathIsTrusted(t *testing.T) {
	p := NewProvider("")
	if !p.Trusted() {
		t.Error("an empty path must disable the check, not withdraw trust")
	}
}

func TestProviderNilIsTrusted(t *testing.T) {
	var p *Provider
	if !p.Trusted() {
		t.Error("a nil provider must report trusted")
	}
}

func TestProviderCachesWithinTTL(t *testing.T) {
	p := newTestProvider(t, "trusted=no\nsource=none\nboot_id=boot-1\n", "boot-1")
	now := time.Unix(1_757_000_000, 0)
	p.now = func() time.Time { return now }

	if p.Trusted() {
		t.Fatal("first read should report untrusted")
	}
	if err := os.WriteFile(p.path, []byte("trusted=yes\nsource=chrony\nboot_id=boot-1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if p.Trusted() {
		t.Error("within the TTL the cached verdict should still be used")
	}
	now = now.Add(p.ttl + time.Second)
	if !p.Trusted() {
		t.Error("after the TTL the new verdict should be picked up")
	}
}

func TestDefaultPath(t *testing.T) {
	if got := DefaultPath("/data/meshsat.db"); got != "/data/clock-state" {
		t.Errorf("DefaultPath = %q", got)
	}
	if got := DefaultPath(""); got != "/data/clock-state" {
		t.Errorf("DefaultPath(empty) = %q", got)
	}
}
