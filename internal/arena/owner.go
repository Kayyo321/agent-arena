package arena

import (
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
)

var identityOnce sync.Once
var ownBoot, ownStart string

func processIdentity(pid int) (boot, start string) {
	if runtime.GOOS == "linux" {
		if b, err := os.ReadFile("/proc/sys/kernel/random/boot_id"); err == nil {
			boot = strings.TrimSpace(string(b))
		}
		if b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat"); err == nil {
			end := strings.LastIndex(string(b), ")")
			if end >= 0 {
				fields := strings.Fields(string(b)[end+1:])
				if len(fields) > 19 {
					start = fields[19]
				}
			}
		}
		return
	}
	if runtime.GOOS == "darwin" {
		if b, err := exec.Command("sysctl", "-n", "kern.boottime").Output(); err == nil {
			boot = strings.TrimSpace(string(b))
			// The numeric timeval is stable across timezone changes; the suffix is not.
			if end := strings.IndexByte(boot, '}'); end >= 0 {
				boot = boot[:end+1]
			}
		}
		cmd := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "lstart=")
		cmd.Env = append(os.Environ(), "TZ=UTC", "LC_ALL=C")
		if b, err := cmd.Output(); err == nil {
			start = strings.TrimSpace(string(b))
		}
	}
	return
}
func ownIdentity() (string, string) {
	identityOnce.Do(func() { ownBoot, ownStart = processIdentity(os.Getpid()) })
	return ownBoot, ownStart
}
