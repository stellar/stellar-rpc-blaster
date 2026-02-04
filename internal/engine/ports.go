package engine

import (
	"bufio"
	"errors"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
)

// EphemeralPortCount returns the total number of available ephemeral ports
// Should this fail, a safe number of 16384 will be used
func EphemeralPortCount() int {
	switch runtime.GOOS {
	case "linux":
		if count, err := linuxPortRange(); err == nil {
			return count
		}
	case "darwin":
		if count, err := darwinPortRange(); err == nil {
			return count
		}
	}
	return 16384
}

// Linux: read from /proc/sys/net/ipv4/ip_local_port_range
func linuxPortRange() (int, error) {
	f, err := os.Open("/proc/sys/net/ipv4/ip_local_port_range")
	if err != nil {
		return 0, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	if scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) >= 2 {
			low, errLow := strconv.Atoi(fields[0])
			high, errHigh := strconv.Atoi(fields[1])
			return high - low + 1, errors.Join(errLow, errHigh)
		}
	}
	return 0, scanner.Err()
}

// macOS: use sysctl
func darwinPortRange() (int, error) {
	lowStr, err := exec.Command("sysctl", "-n", "net.inet.ip.portrange.first").Output()
	if err != nil {
		return 0, err
	}
	highStr, err := exec.Command("sysctl", "-n", "net.inet.ip.portrange.last").Output()
	if err != nil {
		return 0, err
	}

	low, errLow := strconv.Atoi(strings.TrimSpace(string(lowStr)))
	high, errHigh := strconv.Atoi(strings.TrimSpace(string(highStr)))
	return high - low + 1, errors.Join(errLow, errHigh)
}
