package cell

import (
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// VTY runs one command on an Osmocom VTY on the loopback (no login): the
// banner is read first, the command goes CRLF-terminated, the answer is
// read until the prompt. nil lines mean the VTY did not answer at all.
func VTY(port int, command string) ([]string, error) {
	c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 2*time.Second)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	readUntilPrompt := func(idle time.Duration, max int) []byte {
		var data []byte
		buf := make([]byte, 65536)
		quiet := 0
		for quiet < max {
			c.SetReadDeadline(time.Now().Add(idle))
			n, err := c.Read(buf)
			if n > 0 {
				data = append(data, buf[:n]...)
				if strings.HasSuffix(strings.TrimRight(string(data), " "), ">") {
					break
				}
			}
			if err != nil {
				if ne, ok := err.(net.Error); ok && ne.Timeout() {
					quiet++
					continue
				}
				break
			}
		}
		return data
	}
	readUntilPrompt(700*time.Millisecond, 3) // the banner
	if _, err := c.Write([]byte(command + "\r\n")); err != nil {
		return nil, err
	}
	data := readUntilPrompt(time.Second, 2)
	c.Write([]byte("exit\r\n"))
	var lines []string
	for _, ln := range strings.Split(string(data), "\n") {
		ln = strings.TrimRight(ln, "\r")
		t := strings.TrimSpace(ln)
		if t == "" || t == command || strings.HasSuffix(t, ">") {
			continue
		}
		lines = append(lines, ln)
	}
	return lines, nil
}

// BTSState is what osmo-bsc says about bts 0.
type BTSState struct {
	Band  string   `json:"band,omitempty"`
	ARFCN int      `json:"arfcn,omitempty"`
	OML   string   `json:"oml,omitempty"`
	RSL   string   `json:"rsl,omitempty"`
	Raw   []string `json:"raw"`
}

var (
	reOML   = regexp.MustCompile(`OML Link state:\s*(\w+)`)
	reRSL   = regexp.MustCompile(`RSL link state:\s*(\w+)`)
	reBand  = regexp.MustCompile(`in band (\w+)`)
	reARFCN = regexp.MustCompile(`ARFCNs?:\s*(\d+)`)
	reKeep  = regexp.MustCompile(`OML Link|RSL|TRX \d|ARFCNs?:|in band|operational|Administrative|BTS \d is`)
)

// ParseBTS reads "show bts 0".
func ParseBTS(lines []string) BTSState {
	st := BTSState{Raw: []string{}}
	for _, ln := range lines {
		s := strings.TrimSpace(ln)
		if reKeep.MatchString(s) && len(st.Raw) < 12 {
			st.Raw = append(st.Raw, s)
		}
		if m := reOML.FindStringSubmatch(s); m != nil {
			st.OML = m[1]
		}
		if m := reRSL.FindStringSubmatch(s); m != nil {
			st.RSL = m[1]
		}
		if m := reBand.FindStringSubmatch(s); m != nil {
			st.Band = m[1]
		}
		if m := reARFCN.FindStringSubmatch(s); m != nil && st.ARFCN == 0 {
			st.ARFCN, _ = strconv.Atoi(m[1])
		}
	}
	return st
}

// Phone is one subscriber the MSC's VLR knows.
type Phone struct {
	IMSI     string `json:"imsi"`
	MSISDN   string `json:"msisdn,omitempty"`
	LAC      int    `json:"lac,omitempty"`
	LastSeen string `json:"last_seen,omitempty"`
}

var (
	reIMSI   = regexp.MustCompile(`^IMSI:\s*(\d+)`)
	reMSISDN = regexp.MustCompile(`^MSISDN:\s*(\d+)`)
	reLAC    = regexp.MustCompile(`^LAC:\s*(\d+)`)
	reSeen   = regexp.MustCompile(`Last seen:\s*(.+)`)
)

// ParsePhones reads "show subscriber cache".
func ParsePhones(lines []string) []Phone {
	phones := []Phone{}
	var cur *Phone
	for _, ln := range lines {
		s := strings.TrimSpace(ln)
		if m := reIMSI.FindStringSubmatch(s); m != nil {
			phones = append(phones, Phone{IMSI: m[1]})
			cur = &phones[len(phones)-1]
			continue
		}
		if cur == nil {
			continue
		}
		if m := reMSISDN.FindStringSubmatch(s); m != nil {
			cur.MSISDN = m[1]
		} else if m := reLAC.FindStringSubmatch(s); m != nil {
			cur.LAC, _ = strconv.Atoi(m[1])
		} else if m := reSeen.FindStringSubmatch(s); m != nil {
			cur.LastSeen = strings.TrimSpace(m[1])
		}
	}
	return phones
}

// vtyFn is the VTY call the runner uses; tests replace it.
var vtyFn = VTY

// ProvisionHLR creates the subscribers in osmo-hlr over its VTY (an
// existing one is kept) and sets their numbers.
func ProvisionHLR(subs []Subscriber) error {
	for _, s := range subs {
		if lines, err := vtyFn(VTYHLR, "subscriber imsi "+s.IMSI+" create"); err != nil {
			return fmt.Errorf("cell: hlr vty: %w", err)
		} else if bad := vtyError(lines); bad != "" && !strings.Contains(bad, "already exists") {
			return fmt.Errorf("cell: hlr: create %s: %s", s.IMSI, bad)
		}
		if lines, err := vtyFn(VTYHLR, "subscriber imsi "+s.IMSI+" update msisdn "+s.MSISDN); err != nil {
			return fmt.Errorf("cell: hlr vty: %w", err)
		} else if bad := vtyError(lines); bad != "" {
			return fmt.Errorf("cell: hlr: msisdn %s: %s", s.MSISDN, bad)
		}
	}
	return nil
}

func vtyError(lines []string) string {
	for _, ln := range lines {
		t := strings.TrimSpace(ln)
		if strings.HasPrefix(t, "%") {
			return strings.TrimSpace(strings.TrimPrefix(t, "%"))
		}
	}
	return ""
}
