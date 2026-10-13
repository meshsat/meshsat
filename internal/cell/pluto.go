package cell

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

// Pluto is the SDR as sysfs shows it.
type Pluto struct {
	Present   bool   `json:"present"`
	State     string `json:"state,omitempty"` // run or dfu
	USB       string `json:"usb,omitempty"`   // sysfs name, e.g. 9-2
	Iface     string `json:"iface,omitempty"` // the RNDIS network interface
	Reachable bool   `json:"reachable"`
	Build     string `json:"build,omitempty"` // the meshsat-cell image build on it, once checked
}

var plutoIDs = map[string]string{"0456:b673": "run", "0456:b674": "dfu"}

// SysfsUSB is where the USB devices are; tests point it at a tree of their own.
var SysfsUSB = "/sys/bus/usb/devices"

// FindPluto looks for the Pluto on the USB bus.
func FindPluto() Pluto {
	var p Pluto
	entries, err := os.ReadDir(SysfsUSB)
	if err != nil {
		return p
	}
	for _, e := range entries {
		dev := filepath.Join(SysfsUSB, e.Name())
		vid, err1 := os.ReadFile(filepath.Join(dev, "idVendor"))
		pid, err2 := os.ReadFile(filepath.Join(dev, "idProduct"))
		if err1 != nil || err2 != nil {
			continue
		}
		state, ok := plutoIDs[strings.TrimSpace(string(vid))+":"+strings.TrimSpace(string(pid))]
		if !ok {
			continue
		}
		p.Present, p.State, p.USB = true, state, e.Name()
		ifaces, _ := filepath.Glob(filepath.Join(dev, e.Name()+":*", "net", "*"))
		for _, i := range ifaces {
			p.Iface = filepath.Base(i)
		}
		return p
	}
	return p
}

// ipCmd runs iproute2; tests replace it.
var ipCmd = func(ctx context.Context, args ...string) error {
	out, err := exec.CommandContext(ctx, "ip", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("ip %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

// SetupNetwork gives the Pluto's RNDIS interface the host address and a
// host route to the Pluto: a /32 and one route, never the /24 (that subnet
// is in use elsewhere on the owner's estate). Idempotent.
func SetupNetwork(ctx context.Context, iface string) error {
	if iface == "" {
		return errors.New("cell: the Pluto has no network interface yet (rndis_host not bound?)")
	}
	if err := ipCmd(ctx, "link", "set", iface, "up"); err != nil {
		return err
	}
	if err := ipCmd(ctx, "addr", "replace", HostIP+"/32", "dev", iface); err != nil {
		return err
	}
	return ipCmd(ctx, "route", "replace", PlutoIP+"/32", "dev", iface)
}

// Reachable reports whether the Pluto answers on its SSH port.
func Reachable(timeout time.Duration) bool {
	c, err := net.DialTimeout("tcp", net.JoinHostPort(PlutoIP, "22"), timeout)
	if err != nil {
		return false
	}
	c.Close()
	return true
}

// reachableFn is Reachable; tests replace it.
var reachableFn = Reachable

// WaitReachable waits for the Pluto's USB network to come up.
func WaitReachable(ctx context.Context, max time.Duration) bool {
	deadline := time.Now().Add(max)
	for time.Now().Before(deadline) {
		if reachableFn(time.Second) {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(500 * time.Millisecond):
		}
	}
	return false
}

// plutoSSH runs one command on the Pluto as root (the image's fixed
// password, over the USB link only).
var plutoSSH = func(ctx context.Context, command string) (string, error) {
	cfg := &ssh.ClientConfig{
		User:            "root",
		Auth:            []ssh.AuthMethod{ssh.Password("analog")},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), // a USB cable, not a network
		Timeout:         5 * time.Second,
	}
	d := net.Dialer{Timeout: 5 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(PlutoIP, "22"))
	if err != nil {
		return "", err
	}
	c, chans, reqs, err := ssh.NewClientConn(conn, PlutoIP, cfg)
	if err != nil {
		conn.Close()
		return "", err
	}
	client := ssh.NewClient(c, chans, reqs)
	defer client.Close()
	sess, err := client.NewSession()
	if err != nil {
		return "", err
	}
	defer sess.Close()
	out, err := sess.CombinedOutput(command)
	return strings.TrimSpace(string(out)), err
}

// ImageBuild checks that the Pluto runs the meshsat-cell image with
// osmo-trx-pluto (whose own guard refuses any TX LO outside the licensed
// block) and returns the image's build id.
func ImageBuild(ctx context.Context) (string, error) {
	out, err := plutoSSH(ctx, "test -f /etc/meshsat-cell/build && pidof osmo-trx-pluto >/dev/null && cat /etc/meshsat-cell/build")
	if err != nil {
		return "", fmt.Errorf("cell: the Pluto is not running the meshsat-cell image with osmo-trx-pluto: %v", err)
	}
	if len(out) > 12 {
		out = out[:12]
	}
	return out, nil
}

// ParkTX puts the Pluto's transmitter at its minimum gain, where the
// image parks it at boot.
func ParkTX(ctx context.Context) error {
	_, err := plutoSSH(ctx, `for d in /sys/bus/iio/devices/iio:device*; do [ "$(cat $d/name)" = ad9361-phy ] && echo -89.75 > $d/out_voltage0_hardwaregain; done; true`)
	return err
}
