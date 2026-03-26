// Package sysinfo provides lightweight system metrics collection via /proc and syscall.
// No CGO, no external dependencies — reads procfs directly for Linux.
package sysinfo

import (
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// SystemMetrics contains host-level resource utilization.
type SystemMetrics struct {
	CPUPct  float64
	MemPct  float64
	DiskPct float64
	Uptime  time.Duration
}

// Collect reads system metrics from procfs and syscall.
// Returns zero values for any metric that can't be read (e.g., non-Linux).
func Collect(diskPath string) SystemMetrics {
	m := SystemMetrics{}
	m.CPUPct = cpuPercent()
	m.MemPct = memPercent()
	m.DiskPct = diskPercent(diskPath)
	m.Uptime = uptime()
	return m
}

// cpuPercent reads /proc/stat and computes total CPU utilization.
// Uses a 200ms sample window between two reads.
func cpuPercent() float64 {
	idle1, total1 := readCPUStat()
	if total1 == 0 {
		return 0
	}
	time.Sleep(200 * time.Millisecond)
	idle2, total2 := readCPUStat()
	if total2 == total1 {
		return 0
	}
	idleDelta := float64(idle2 - idle1)
	totalDelta := float64(total2 - total1)
	return (1.0 - idleDelta/totalDelta) * 100
}

func readCPUStat() (idle, total uint64) {
	data, err := os.ReadFile("/proc/stat")
	if err != nil {
		return 0, 0
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "cpu ") {
			fields := strings.Fields(line)
			if len(fields) < 5 {
				return 0, 0
			}
			for i := 1; i < len(fields); i++ {
				v, _ := strconv.ParseUint(fields[i], 10, 64)
				total += v
				if i == 4 { // idle is the 4th value (index 4)
					idle = v
				}
			}
			return idle, total
		}
	}
	return 0, 0
}

// memPercent reads /proc/meminfo and computes used memory percentage.
func memPercent() float64 {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0
	}
	var memTotal, memAvailable uint64
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "MemTotal:") {
			memTotal = parseMemInfoValue(line)
		} else if strings.HasPrefix(line, "MemAvailable:") {
			memAvailable = parseMemInfoValue(line)
		}
	}
	if memTotal == 0 {
		return 0
	}
	used := memTotal - memAvailable
	return float64(used) / float64(memTotal) * 100
}

func parseMemInfoValue(line string) uint64 {
	fields := strings.Fields(line)
	if len(fields) < 2 {
		return 0
	}
	v, _ := strconv.ParseUint(fields[1], 10, 64)
	return v
}

// diskPercent uses Statfs to compute disk utilization for the given path.
func diskPercent(path string) float64 {
	if path == "" {
		path = "/"
	}
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return 0
	}
	total := stat.Blocks * uint64(stat.Bsize)
	free := stat.Bfree * uint64(stat.Bsize)
	if total == 0 {
		return 0
	}
	used := total - free
	return float64(used) / float64(total) * 100
}

// uptime reads /proc/uptime and returns the system uptime.
func uptime() time.Duration {
	data, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0
	}
	fields := strings.Fields(string(data))
	if len(fields) < 1 {
		return 0
	}
	secs, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return 0
	}
	return time.Duration(secs * float64(time.Second))
}
