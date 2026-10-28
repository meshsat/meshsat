package cell

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

// Daemons in start order; the radio is the last and separate.
var coreDaemons = []string{"osmo-stp", "osmo-hlr", "osmo-mgw", "osmo-msc", "osmo-cbc", "osmo-bsc"}

const radioDaemon = "osmo-bts-trx"

// Available reports whether the Osmocom daemons are installed (the Bridge
// image carries them; a bare binary elsewhere may not).
func Available(binDir string) bool {
	for _, d := range append(append([]string{}, coreDaemons...), radioDaemon) {
		if _, ok := lookPath(binDir, d); !ok {
			return false
		}
	}
	return true
}

// TXRow is one line of the transmit log: a key-up or a key-down.
type TXRow struct {
	At       time.Time `json:"at"`
	Event    string    `json:"event"` // up, down, refused
	ARFCN    int       `json:"arfcn"`
	FreqMHz  float64   `json:"freq_mhz"`
	Setting  string    `json:"setting"`
	Output   string    `json:"output"`
	Purpose  string    `json:"purpose"`
	Operator string    `json:"operator"`
	Licence  string    `json:"licence"`
}

// TXLog keeps the rows (the Bridge's database).
type TXLog interface {
	AppendTX(row TXRow) error
}

// Status is the cell as the runner sees it; the same shape the kit-side
// cellctl reports, so the UI reads one thing.
type Status struct {
	Pluto     Pluto       `json:"pluto"`
	Core      CoreStatus  `json:"core"`
	Radio     RadioStatus `json:"radio"`
	BTS       *BTSState   `json:"bts"`
	Phones    []Phone     `json:"phones"`
	ARFCN     []int       `json:"arfcn"`
	SMPP      SMPPInfo    `json:"smpp"`
	Licence   LicenceInfo `json:"licence"`
	AutoRadio bool        `json:"auto_radio"`
	Mode      string      `json:"mode"` // "bridge": the daemons run inside the Bridge
	LastError string      `json:"last_error,omitempty"`
}

// CoreStatus is the core daemons.
type CoreStatus struct {
	Units map[string]bool `json:"units"`
	Up    bool            `json:"up"`
}

// RadioStatus is osmo-bts-trx.
type RadioStatus struct {
	Active bool       `json:"active"`
	Since  *time.Time `json:"since,omitempty"`
}

// SMPPInfo is where the Bridge's gateway binds.
type SMPPInfo struct {
	Bind        string `json:"bind"`
	Port        int    `json:"port"`
	SystemID    string `json:"system_id"`
	ShortCode   string `json:"short_code"`
	PasswordSet bool   `json:"password_set"`
}

// LicenceInfo is what the TX log cites.
type LicenceInfo struct {
	Number string `json:"number"`
	Band   string `json:"band"`
	ARFCN  string `json:"arfcn_block"`
}

// Runner runs the cell.
type Runner struct {
	cfg   Config
	txlog TXLog

	mu       sync.Mutex
	up       bool
	procs    map[string]*process
	radio    *process
	confDir  string
	pluto    Pluto
	lastErr  string
	radioErr string
	ctx      context.Context
	cancel   context.CancelFunc

	onRadioStop func()
}

// New makes a runner; Up starts it.
func New(cfg Config, txlog TXLog) (*Runner, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &Runner{cfg: cfg, txlog: txlog, procs: map[string]*process{}}, nil
}

// Password is the SMPP password the gateway binds with.
func (r *Runner) Password() string { return r.cfg.SMPPPassword }

// SetRadioStopHook runs fn after the radio keyed down (the cell broadcast
// client takes its messages off osmo-cbc then, or they would go out again
// unlogged on the next key-up). [MESHSAT-1734]
func (r *Runner) SetRadioStopHook(fn func()) {
	r.mu.Lock()
	r.onRadioStop = fn
	r.mu.Unlock()
}

// Config returns the configuration.
func (r *Runner) Config() Config { return r.cfg }

func (r *Runner) setErr(err error) {
	r.mu.Lock()
	if err != nil {
		r.lastErr = err.Error()
	} else {
		r.lastErr = ""
	}
	r.mu.Unlock()
}

// Up brings the cell up for the Pluto found on USB: its network, the
// rendered configs, the core, the SIMs in the HLR, and the radio when
// AutoRadio is on. The core staying up without the radio is not a failure.
func (r *Runner) Up(ctx context.Context) error {
	if err := r.UpCore(ctx); err != nil {
		return err
	}
	if r.cfg.AutoRadio && !r.RadioActive() {
		purpose := fmt.Sprintf("auto: the cell's SDR was plugged into %s", r.cfg.Operator)
		if err := r.RadioStart(ctx, purpose); err != nil {
			log.Warn().Err(err).Msg("cell: the core is up, the radio is not")
		}
	}
	return nil
}

// UpCore brings everything but the radio up.
func (r *Runner) UpCore(ctx context.Context) error {
	r.mu.Lock()
	if r.up {
		r.mu.Unlock()
		return nil
	}
	r.up = true
	r.ctx, r.cancel = context.WithCancel(context.Background())
	r.mu.Unlock()

	p := FindPluto()
	r.mu.Lock()
	r.pluto = p
	r.mu.Unlock()
	if !p.Present {
		r.setErr(errors.New("cell: no Pluto on USB"))
		return errors.New("cell: no Pluto on USB")
	}
	if p.State == "dfu" {
		err := errors.New("cell: the Pluto is in DFU mode; power-cycle it")
		r.setErr(err)
		return err
	}
	if err := SetupNetwork(ctx, p.Iface); err != nil {
		r.setErr(err)
		return err
	}
	confDir, err := r.cfg.Render()
	if err != nil {
		r.setErr(err)
		return err
	}
	r.mu.Lock()
	r.confDir = confDir
	r.mu.Unlock()
	if err := r.startCore(); err != nil {
		r.setErr(err)
		return err
	}
	// The HLR answers on its VTY a moment after start; the SIMs go in then.
	if err := r.provision(ctx); err != nil {
		r.setErr(err)
		log.Warn().Err(err).Msg("cell: the SIMs were not provisioned; the core runs")
	} else {
		r.setErr(nil)
	}
	return nil
}

func (r *Runner) startCore() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, d := range coreDaemons {
		if _, ok := r.procs[d]; ok {
			continue
		}
		bin, ok := lookPath(r.cfg.BinDir, d)
		if !ok {
			return fmt.Errorf("cell: %s is not installed in this image", d)
		}
		argv := []string{bin, "-c", filepath.Join(r.confDir, d+".cfg")}
		if d == "osmo-hlr" {
			db := filepath.Join(r.cfg.DataDir, "hlr.db")
			if _, err := os.Stat(db); err != nil {
				if tool, ok := lookPath(r.cfg.BinDir, "osmo-hlr-db-tool"); ok {
					if err := runOnce(tool, "-l", db, "create"); err != nil {
						return fmt.Errorf("cell: hlr.db: %w", err)
					}
				}
			}
			argv = append(argv, "-l", db, "-U")
		}
		p := newProcess(d, r.cfg.DataDir, argv...)
		r.procs[d] = p
		p.start(r.ctx)
		// The next daemon connects to this one: give it a breath.
		time.Sleep(300 * time.Millisecond)
	}
	return nil
}

func (r *Runner) provision(ctx context.Context) error {
	var last error
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if err := ProvisionHLR(r.cfg.Subscribers); err == nil {
			return nil
		} else {
			last = err
		}
		if !sleepCtx(ctx, time.Second) {
			return ctx.Err()
		}
	}
	return last
}

// RadioStart keys the radio up: the preflight (the Pluto reachable and on
// our image), the TX-log row, then osmo-bts-trx. The licence terms are
// already in the rendered config (band DCS1800, the ARFCN in the block,
// the bare Pluto's power setting).
func (r *Runner) RadioStart(ctx context.Context, purpose string) error {
	r.mu.Lock()
	if !r.up {
		r.mu.Unlock()
		return errors.New("cell: the core is not up")
	}
	if r.radio != nil && r.radio.wanted.Load() {
		r.mu.Unlock()
		return nil
	}
	r.mu.Unlock()
	if len(purpose) < 3 {
		return errors.New("cell: a purpose for the TX log is required")
	}
	p := FindPluto()
	if !p.Present || p.State != "run" {
		err := errors.New("cell: no Pluto on USB in run mode")
		r.setRadioErr(err)
		return err
	}
	if !WaitReachable(ctx, 20*time.Second) {
		err := fmt.Errorf("cell: the Pluto does not answer on %s", PlutoIP)
		r.setRadioErr(err)
		return err
	}
	build, err := ImageBuild(ctx)
	if err != nil {
		r.setRadioErr(err)
		r.logTX("refused", purpose+" ("+err.Error()+")", "")
		return err
	}
	p.Reachable, p.Build = true, build
	r.mu.Lock()
	r.pluto = p
	r.mu.Unlock()
	bin, ok := lookPath(r.cfg.BinDir, radioDaemon)
	if !ok {
		return fmt.Errorf("cell: %s is not installed in this image", radioDaemon)
	}
	// Logged before the radio keys up, so no emission goes unrecorded.
	if err := r.logTX("up", purpose, build); err != nil {
		r.setRadioErr(err)
		return fmt.Errorf("cell: the TX log could not be written, the radio stays off: %w", err)
	}
	os.Remove("/tmp/pcu_bts") // a stale PCU socket crash-loops osmo-bts-trx
	argv := []string{bin, "-s", "-c", filepath.Join(r.confDir, radioDaemon+".cfg")}
	if chrt, ok := lookPath("", "chrt"); ok && os.Geteuid() == 0 {
		argv = append([]string{chrt, "-r", "11"}, argv...) // real-time scheduling, as the packaged unit does
	}
	r.mu.Lock()
	r.radio = newProcess(radioDaemon, r.cfg.DataDir, argv...)
	r.radio.start(r.ctx)
	r.radioErr = ""
	r.mu.Unlock()
	log.Info().Int("arfcn", r.cfg.ARFCN).Str("purpose", purpose).Msg("cell: radio keyed up")
	return nil
}

func (r *Runner) setRadioErr(err error) {
	r.mu.Lock()
	r.radioErr = err.Error()
	r.mu.Unlock()
}

// RadioStop keys the radio down, writes the closing row and parks the
// Pluto's transmitter.
func (r *Runner) RadioStop(ctx context.Context) error {
	r.mu.Lock()
	radio := r.radio
	r.radio = nil
	r.mu.Unlock()
	if radio == nil {
		return nil
	}
	was := radio.wanted.Load()
	radio.stop()
	os.Remove("/tmp/pcu_bts") // osmo-bts-trx leaves its PCU socket behind
	if was {
		r.logTX("down", "", "")
	}
	if err := ParkTX(ctx); err != nil {
		log.Debug().Err(err).Msg("cell: the Pluto's transmitter was not parked")
	}
	log.Info().Msg("cell: radio keyed down")
	r.mu.Lock()
	hook := r.onRadioStop
	r.mu.Unlock()
	if hook != nil {
		hook()
	}
	return nil
}

// Down stops the radio and the core.
func (r *Runner) Down(ctx context.Context) {
	r.RadioStop(ctx)
	r.mu.Lock()
	procs := r.procs
	r.procs = map[string]*process{}
	cancel := r.cancel
	r.up = false
	r.mu.Unlock()
	for i := len(coreDaemons) - 1; i >= 0; i-- {
		if p, ok := procs[coreDaemons[i]]; ok {
			p.stop()
		}
	}
	if cancel != nil {
		cancel()
	}
	log.Info().Msg("cell: core stopped")
}

// RadioActive reports whether the radio is wanted and running.
func (r *Runner) RadioActive() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.radio != nil && r.radio.running()
}

// IsUp reports whether the core is wanted.
func (r *Runner) IsUp() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.up
}

func (r *Runner) logTX(event, purpose, build string) error {
	if r.txlog == nil {
		return nil
	}
	licence := r.cfg.Licence
	if licence == "" {
		licence = "not set"
	}
	setting := fmt.Sprintf("osmo-bts-trx, nominal power 23, max_power_red 0 (Pluto bare, image %s)", build)
	if event == "down" {
		setting = "osmo-bts-trx stopped, Pluto TX parked"
	}
	return r.txlog.AppendTX(TXRow{
		At: time.Now().UTC(), Event: event, ARFCN: r.cfg.ARFCN, FreqMHz: FreqMHz(r.cfg.ARFCN),
		Setting: setting, Output: "<< 200 mW ERP", Purpose: purpose, Operator: r.cfg.Operator, Licence: licence,
	})
}

// Status reports everything, asking the VTYs when the core is up.
func (r *Runner) Status() Status {
	r.mu.Lock()
	up := r.up
	procs := make(map[string]*process, len(r.procs))
	for k, v := range r.procs {
		procs[k] = v
	}
	radio := r.radio
	pluto := r.pluto
	lastErr := r.lastErr
	radioErr := r.radioErr
	r.mu.Unlock()

	st := Status{
		Pluto:     pluto,
		Core:      CoreStatus{Units: map[string]bool{}, Up: up},
		Phones:    []Phone{},
		ARFCN:     []int{r.cfg.ARFCN},
		SMPP:      SMPPInfo{Bind: "127.0.0.1", Port: SMPPPort, SystemID: r.cfg.SMPPSystemID, ShortCode: r.cfg.ShortCode, PasswordSet: r.cfg.SMPPPassword != ""},
		Licence:   LicenceInfo{Number: r.cfg.Licence, Band: Band, ARFCN: fmt.Sprintf("%d-%d", ARFCNMin, ARFCNMax)},
		AutoRadio: r.cfg.AutoRadio,
		Mode:      "bridge",
		LastError: lastErr,
	}
	if !up {
		st.Pluto = FindPluto()
	}
	for _, d := range coreDaemons {
		p, ok := procs[d]
		st.Core.Units[d] = ok && p.running()
		if !st.Core.Units[d] {
			st.Core.Up = false
		}
	}
	if radio != nil && radio.running() {
		st.Radio.Active = true
		since := radio.upSince()
		st.Radio.Since = &since
	}
	if radioErr != "" && !st.Radio.Active {
		st.LastError = radioErr
	}
	if st.Core.Units["osmo-bsc"] {
		if lines, err := vtyFn(VTYBSC, "show bts 0"); err == nil {
			b := ParseBTS(lines)
			st.BTS = &b
		}
	}
	if st.Core.Units["osmo-msc"] {
		if lines, err := vtyFn(VTYMSC, "show subscriber cache"); err == nil {
			st.Phones = ParsePhones(lines)
		}
	}
	return st
}

func runOnce(bin string, args ...string) error {
	out, err := exec.Command(bin, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %v: %s", bin, err, string(out))
	}
	return nil
}
