package transport

// jsprHelper manages the jspr-helper C subprocess for RockBLOCK 9704 serial I/O.
//
// Go's runtime interferes with serial I/O syscalls (select/read behave
// differently than in C). The C helper handles all serial communication and
// forwards JSPR messages via stdin/stdout JSON lines.
//
// Protocol:
//   Go → helper (stdin):  {"cmd":"send","method":"GET","target":"apiVersion","json":"{}"}
//   helper → Go (stdout): {"type":"response","code":200,"target":"apiVersion","json":{...}}
//   helper → Go (stdout): {"type":"unsolicited","code":299,"target":"constellationState","json":{...}}

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

// jsprHelperMsg is a JSON message from the C helper's stdout.
type jsprHelperMsg struct {
	Type   string          `json:"type"` // "response", "unsolicited", "error"
	Code   int             `json:"code"`
	Target string          `json:"target"`
	JSON   json.RawMessage `json:"json"`
}

// jsprHelperPort wraps the C subprocess and implements jsprPort.
// readOneLine calls Read(buf[0:1]) byte-by-byte, so we buffer complete
// JSPR lines and serve them one byte at a time.
type jsprHelperPort struct {
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	stdout  io.ReadCloser
	scanner *bufio.Scanner

	// Byte buffer — complete JSPR lines queued for byte-by-byte Read()
	mu       sync.Mutex
	byteBuf  []byte
	byteCond *sync.Cond

	// Reader goroutine
	done chan struct{}

	port     string
	lastRead time.Time
}

// startJSPRHelper launches the C helper subprocess.
func startJSPRHelper(helperPath, serialPort string, baud int) (*jsprHelperPort, error) {
	cmd := exec.Command(helperPath, serialPort, fmt.Sprintf("%d", baud))

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("stdin pipe: %w", err)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		stdin.Close()
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}

	// Helper's stderr goes to our stderr (for debug logging)
	cmd.Stderr = nil // inherit

	if err := cmd.Start(); err != nil {
		stdin.Close()
		stdout.Close()
		return nil, fmt.Errorf("start helper: %w", err)
	}

	h := &jsprHelperPort{
		cmd:      cmd,
		stdin:    stdin,
		stdout:   stdout,
		scanner:  bufio.NewScanner(stdout),
		done:     make(chan struct{}),
		port:     serialPort,
		lastRead: time.Now(),
	}
	h.byteCond = sync.NewCond(&h.mu)

	// Start reading helper's stdout in background
	go h.readLoop()

	log.Info().Str("port", serialPort).Int("pid", cmd.Process.Pid).Msg("imt: jspr-helper started")
	return h, nil
}

// readLoop reads JSON lines from the helper's stdout, converts them to
// JSPR wire format ("CODE target {json}\r"), and appends to the byte buffer.
func (h *jsprHelperPort) readLoop() {
	defer close(h.done)

	h.scanner.Buffer(make([]byte, 64*1024), 64*1024)

	for h.scanner.Scan() {
		line := h.scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var msg jsprHelperMsg
		if err := json.Unmarshal(line, &msg); err != nil {
			log.Debug().Err(err).Str("line", string(line)).Msg("imt: helper output parse error")
			continue
		}

		// Format as JSPR wire line: "CODE target {json}\r"
		jsonStr := string(msg.JSON)
		if jsonStr == "" || jsonStr == "null" {
			jsonStr = "{}"
		}
		jsprLine := fmt.Sprintf("%d %s %s\r", msg.Code, msg.Target, jsonStr)

		h.mu.Lock()
		h.byteBuf = append(h.byteBuf, []byte(jsprLine)...)
		h.lastRead = time.Now()
		h.mu.Unlock()
		h.byteCond.Signal()

		log.Debug().Int("code", msg.Code).Str("target", msg.Target).Msg("imt: helper received")
	}

	if err := h.scanner.Err(); err != nil {
		log.Warn().Err(err).Msg("imt: helper stdout read error")
	}
}

// Read implements jsprPort. Serves bytes from the buffer, blocking until
// data is available or 500ms timeout. readOneLine calls this with a 1-byte
// buffer to read byte-by-byte.
func (h *jsprHelperPort) Read(buf []byte) (int, error) {
	h.mu.Lock()

	if len(h.byteBuf) == 0 {
		// Wait with timeout
		ch := make(chan struct{}, 1)
		go func() {
			h.mu.Lock()
			for len(h.byteBuf) == 0 {
				h.byteCond.Wait()
			}
			h.mu.Unlock()
			select {
			case ch <- struct{}{}:
			default:
			}
		}()
		h.mu.Unlock()

		select {
		case <-ch:
			h.mu.Lock()
		case <-time.After(500 * time.Millisecond):
			h.byteCond.Broadcast()
			return 0, nil // timeout
		}
	}

	// Copy as many bytes as buf can hold
	n := copy(buf, h.byteBuf)
	h.byteBuf = h.byteBuf[n:]
	h.mu.Unlock()

	return n, nil
}

// Write implements jsprPort. Sends a JSPR command to the helper via stdin.
// The input is a raw JSPR line like "GET apiVersion {}\r".
func (h *jsprHelperPort) Write(data []byte) (int, error) {
	// Parse the JSPR line: "METHOD target {json}\r"
	line := string(data)
	if len(line) > 0 && line[len(line)-1] == '\r' {
		line = line[:len(line)-1]
	}

	// Split into method, target, json
	parts := splitJSPRLine(line)
	if len(parts) < 2 {
		return 0, fmt.Errorf("invalid JSPR line: %q", line)
	}

	method := parts[0]
	target := parts[1]
	jsonPart := "{}"
	if len(parts) >= 3 {
		jsonPart = parts[2]
	}

	// Send as JSON to helper's stdin
	cmd := fmt.Sprintf(`{"cmd":"send","method":"%s","target":"%s","json":"%s"}`+"\n",
		method, target, jsonPart)

	_, err := h.stdin.Write([]byte(cmd))
	if err != nil {
		return 0, fmt.Errorf("write to helper: %w", err)
	}

	return len(data), nil
}

// splitJSPRLine splits "METHOD target {json}" into parts.
func splitJSPRLine(line string) []string {
	// Find first space (after METHOD)
	i := 0
	for i < len(line) && line[i] != ' ' {
		i++
	}
	if i >= len(line) {
		return []string{line}
	}
	method := line[:i]
	rest := line[i+1:]

	// Find second space (after target)
	j := 0
	for j < len(rest) && rest[j] != ' ' {
		j++
	}
	if j >= len(rest) {
		return []string{method, rest}
	}
	target := rest[:j]
	jsonPart := rest[j+1:]

	return []string{method, target, jsonPart}
}

// SetReadTimeout — no-op, timeout handled in Read().
func (h *jsprHelperPort) SetReadTimeout(d time.Duration) error {
	return nil
}

// Close kills the helper subprocess.
func (h *jsprHelperPort) Close() error {
	h.stdin.Close()
	h.cmd.Process.Kill()
	h.cmd.Wait()
	<-h.done
	log.Info().Msg("imt: jspr-helper stopped")
	return nil
}

// LastRead returns when data was last received from the helper.
func (h *jsprHelperPort) LastRead() time.Time {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.lastRead
}

// Path returns the serial port path.
func (h *jsprHelperPort) Path() string {
	return h.port
}
