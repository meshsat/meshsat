package oob

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"time"
)

// ErrAgentUnavailable is returned when the host agent socket is absent.
var ErrAgentUnavailable = errors.New("oob: host agent unavailable")

// HostClient talks to the host agent (deploy/oob/meshsat-oob-agent) over a
// Unix socket: one JSON line request, one JSON line reply, one connection
// per call. The agent's own allowlist is the second gate; this client never
// builds shell strings.
type HostClient struct {
	Path    string
	Timeout time.Duration
}

// NewHostClient returns a client for the given socket path.
func NewHostClient(path string) *HostClient {
	return &HostClient{Path: path, Timeout: 30 * time.Second}
}

type agentRequest struct {
	Action string         `json:"action"`
	Args   map[string]any `json:"args,omitempty"`
}

type agentReply struct {
	OK      bool           `json:"ok"`
	Result  map[string]any `json:"result,omitempty"`
	Error   string         `json:"error,omitempty"`
	Version string         `json:"version,omitempty"`
}

// Available reports whether the socket exists. It is cheap and is checked
// before every host action so the bridge degrades cleanly when the agent
// is not installed or the directory is not mounted.
func (c *HostClient) Available() bool {
	if c == nil || c.Path == "" {
		return false
	}
	fi, err := os.Stat(c.Path)
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeSocket != 0
}

// Call performs one action. The context bounds the whole exchange.
func (c *HostClient) Call(ctx context.Context, action string, args map[string]any) (map[string]any, error) {
	if !c.Available() {
		return nil, ErrAgentUnavailable
	}
	timeout := c.Timeout
	if deadline, ok := ctx.Deadline(); ok {
		if d := time.Until(deadline); d < timeout {
			timeout = d
		}
	}
	dialer := net.Dialer{Timeout: 2 * time.Second}
	conn, err := dialer.DialContext(ctx, "unix", c.Path)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrAgentUnavailable, err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(timeout))

	line, err := json.Marshal(agentRequest{Action: action, Args: args})
	if err != nil {
		return nil, err
	}
	if _, err := conn.Write(append(line, '\n')); err != nil {
		return nil, fmt.Errorf("oob: agent write: %w", err)
	}
	r := bufio.NewReader(conn)
	resp, err := r.ReadBytes('\n')
	if err != nil && len(resp) == 0 {
		return nil, fmt.Errorf("oob: agent read: %w", err)
	}
	var reply agentReply
	if err := json.Unmarshal(resp, &reply); err != nil {
		return nil, fmt.Errorf("oob: agent reply: %w", err)
	}
	if !reply.OK {
		if reply.Error == "" {
			reply.Error = "agent refused"
		}
		return reply.Result, fmt.Errorf("oob: agent: %s", reply.Error)
	}
	if reply.Result == nil {
		reply.Result = map[string]any{}
	}
	if reply.Version != "" {
		if _, ok := reply.Result["version"]; !ok {
			reply.Result["version"] = reply.Version
		}
	}
	return reply.Result, nil
}

// Ping returns the agent version.
func (c *HostClient) Ping(ctx context.Context) (string, error) {
	res, err := c.Call(ctx, "ping", nil)
	if err != nil {
		return "", err
	}
	if v, ok := res["version"].(string); ok {
		return v, nil
	}
	return "unknown", nil
}
