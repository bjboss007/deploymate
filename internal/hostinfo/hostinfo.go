// Package hostinfo reads the health of the machine DeployMate runs on: CPU,
// memory, disks, network, temperature and power. Everything comes from /proc and
// /sys (plus statfs), so it needs no privileges and no extra software. The
// reader takes a root directory so tests can point it at a fixture tree.
package hostinfo

import (
	"bufio"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Snapshot is one reading of the host. Fields that could not be read keep
// their zero value and the matching *Known flag stays false, so the page can
// say "not available" instead of showing a misleading 0.
type Snapshot struct {
	Supported bool // /proc is readable (Linux); false on a Mac dev machine
	Time      time.Time

	Cores    int
	CPUPct   float64 // 0-100 across all cores
	CPUKnown bool
	Load1    float64
	Load5    float64
	Load15   float64

	MemTotal, MemAvailable, MemCached uint64
	SwapTotal, SwapUsed               uint64
	MemPressure                       float64 // % of time some task stalled on memory, 10s average
	PressureKnown                     bool

	Disks []Disk

	NetRxBps, NetTxBps uint64 // bytes per second since the previous reading
	NetKnown           bool

	Uptime         time.Duration
	OS, Kernel     string
	RebootRequired bool
	TempC          float64 // hottest thermal zone
	TempKnown      bool
	Power          Power
}

// Disk is one filesystem.
type Disk struct {
	Label string
	Path  string
	Total uint64
	Free  uint64 // available to non-root users
}

// Used returns bytes in use.
func (d Disk) Used() uint64 {
	if d.Free > d.Total {
		return 0
	}
	return d.Total - d.Free
}

// UsedPct is the percentage of the filesystem in use.
func (d Disk) UsedPct() float64 {
	if d.Total == 0 {
		return 0
	}
	return float64(d.Used()) / float64(d.Total) * 100
}

// Power is the battery/mains state; Present is false on machines without a battery.
type Power struct {
	Present   bool
	OnBattery bool
	Percent   int
}

// MemUsed is memory in use, not counting reclaimable cache.
func (s Snapshot) MemUsed() uint64 {
	if s.MemAvailable > s.MemTotal {
		return 0
	}
	return s.MemTotal - s.MemAvailable
}

// MemUsedPct is MemUsed as a percentage of total.
func (s Snapshot) MemUsedPct() float64 {
	if s.MemTotal == 0 {
		return 0
	}
	return float64(s.MemUsed()) / float64(s.MemTotal) * 100
}

// Collector reads snapshots. CPU and network are rates, so it remembers the
// previous reading between calls.
type Collector struct {
	// Root is the filesystem root to read /proc and /sys from ("" = "/").
	Root string
	// Paths are filesystems to report, in order; entries on the same device
	// are reported once.
	Paths []DiskPath
	// Cores overrides runtime.NumCPU (tests).
	Cores int

	mu       sync.Mutex
	prevCPU  cpuTimes
	prevNet  netCounters
	prevTime time.Time
}

// DiskPath names a path to measure.
type DiskPath struct{ Label, Path string }

type cpuTimes struct{ total, idle uint64 }
type netCounters struct{ rx, tx uint64 }

func (c *Collector) path(rel string) string {
	root := c.Root
	if root == "" {
		root = "/"
	}
	return filepath.Join(root, rel)
}

func (c *Collector) read(rel string) (string, bool) {
	b, err := os.ReadFile(c.path(rel))
	if err != nil {
		return "", false
	}
	return string(b), true
}

// Snapshot reads the host now. The first call takes a short second reading to
// work out CPU and network rates; later calls compare with the previous one.
func (c *Collector) Snapshot(ctx context.Context) Snapshot {
	c.mu.Lock()
	defer c.mu.Unlock()

	s := Snapshot{Time: time.Now()}
	if _, ok := c.read("proc/meminfo"); !ok {
		return s
	}
	s.Supported = true
	s.Cores = c.Cores
	if s.Cores == 0 {
		s.Cores = runtime.NumCPU()
	}

	if c.prevTime.IsZero() {
		c.prevCPU, _ = c.cpu()
		c.prevNet, _ = c.net()
		c.prevTime = time.Now()
		select {
		case <-ctx.Done():
		case <-time.After(250 * time.Millisecond):
		}
		s.Time = time.Now()
	}

	if cur, ok := c.cpu(); ok {
		if dt := cur.total - c.prevCPU.total; cur.total > c.prevCPU.total && dt > 0 {
			idle := cur.idle - c.prevCPU.idle
			if idle > dt {
				idle = dt
			}
			s.CPUPct = float64(dt-idle) / float64(dt) * 100
			s.CPUKnown = true
		}
		c.prevCPU = cur
	}
	if cur, ok := c.net(); ok {
		if el := s.Time.Sub(c.prevTime).Seconds(); el > 0 && cur.rx >= c.prevNet.rx && cur.tx >= c.prevNet.tx {
			s.NetRxBps = uint64(float64(cur.rx-c.prevNet.rx) / el)
			s.NetTxBps = uint64(float64(cur.tx-c.prevNet.tx) / el)
			s.NetKnown = true
		}
		c.prevNet = cur
	}
	c.prevTime = s.Time

	if v, ok := c.read("proc/loadavg"); ok {
		f := strings.Fields(v)
		if len(f) >= 3 {
			s.Load1, _ = strconv.ParseFloat(f[0], 64)
			s.Load5, _ = strconv.ParseFloat(f[1], 64)
			s.Load15, _ = strconv.ParseFloat(f[2], 64)
		}
	}
	c.memory(&s)
	s.Disks = c.disks()
	c.machine(&s)
	return s
}

func (c *Collector) cpu() (cpuTimes, bool) {
	v, ok := c.read("proc/stat")
	if !ok {
		return cpuTimes{}, false
	}
	line, _, _ := strings.Cut(v, "\n")
	f := strings.Fields(line)
	if len(f) < 5 || f[0] != "cpu" {
		return cpuTimes{}, false
	}
	var t cpuTimes
	for i, x := range f[1:] {
		n, err := strconv.ParseUint(x, 10, 64)
		if err != nil {
			return cpuTimes{}, false
		}
		// user nice system idle iowait irq softirq steal; guest time is already
		// counted inside user, so stop after the eighth field.
		if i >= 8 {
			break
		}
		t.total += n
		if i == 3 || i == 4 {
			t.idle += n
		}
	}
	return t, true
}

func (c *Collector) net() (netCounters, bool) {
	v, ok := c.read("proc/net/dev")
	if !ok {
		return netCounters{}, false
	}
	var n netCounters
	sc := bufio.NewScanner(strings.NewReader(v))
	for sc.Scan() {
		name, rest, found := strings.Cut(sc.Text(), ":")
		if !found {
			continue
		}
		name = strings.TrimSpace(name)
		// Skip loopback and the virtual interfaces Docker makes: their traffic is
		// the same bytes again, already counted on the real interface.
		if name == "lo" || name == "docker0" || strings.HasPrefix(name, "veth") || strings.HasPrefix(name, "br-") {
			continue
		}
		f := strings.Fields(rest)
		if len(f) < 9 {
			continue
		}
		rx, _ := strconv.ParseUint(f[0], 10, 64)
		tx, _ := strconv.ParseUint(f[8], 10, 64)
		n.rx += rx
		n.tx += tx
	}
	return n, true
}

func (c *Collector) memory(s *Snapshot) {
	if v, ok := c.read("proc/meminfo"); ok {
		kb := map[string]uint64{}
		for _, line := range strings.Split(v, "\n") {
			k, rest, found := strings.Cut(line, ":")
			if !found {
				continue
			}
			f := strings.Fields(rest)
			if len(f) > 0 {
				n, _ := strconv.ParseUint(f[0], 10, 64)
				kb[k] = n * 1024
			}
		}
		s.MemTotal = kb["MemTotal"]
		s.MemAvailable = kb["MemAvailable"]
		if _, has := kb["MemAvailable"]; !has { // very old kernels
			s.MemAvailable = kb["MemFree"] + kb["Buffers"] + kb["Cached"]
		}
		s.MemCached = kb["Cached"] + kb["Buffers"]
		s.SwapTotal = kb["SwapTotal"]
		if kb["SwapFree"] <= kb["SwapTotal"] {
			s.SwapUsed = kb["SwapTotal"] - kb["SwapFree"]
		}
	}
	if v, ok := c.read("proc/pressure/memory"); ok {
		for _, line := range strings.Split(v, "\n") {
			if !strings.HasPrefix(line, "some ") {
				continue
			}
			for _, f := range strings.Fields(line) {
				if x, found := strings.CutPrefix(f, "avg10="); found {
					if p, err := strconv.ParseFloat(x, 64); err == nil {
						s.MemPressure, s.PressureKnown = p, true
					}
				}
			}
		}
	}
}

func (c *Collector) disks() []Disk {
	var out []Disk
	seen := map[uint64]bool{}
	for _, p := range c.Paths {
		var st syscall.Stat_t
		if err := syscall.Stat(p.Path, &st); err != nil {
			continue
		}
		dev := uint64(st.Dev)
		if seen[dev] {
			continue
		}
		var fs syscall.Statfs_t
		if err := syscall.Statfs(p.Path, &fs); err != nil {
			continue
		}
		seen[dev] = true
		bs := uint64(fs.Bsize)
		out = append(out, Disk{Label: p.Label, Path: p.Path, Total: uint64(fs.Blocks) * bs, Free: uint64(fs.Bavail) * bs})
	}
	return out
}

func (c *Collector) machine(s *Snapshot) {
	if v, ok := c.read("proc/uptime"); ok {
		if f := strings.Fields(v); len(f) > 0 {
			if secs, err := strconv.ParseFloat(f[0], 64); err == nil {
				s.Uptime = time.Duration(secs * float64(time.Second))
			}
		}
	}
	if v, ok := c.read("etc/os-release"); ok {
		for _, line := range strings.Split(v, "\n") {
			if x, found := strings.CutPrefix(line, "PRETTY_NAME="); found {
				s.OS = strings.Trim(x, `"`)
			}
		}
	}
	if v, ok := c.read("proc/sys/kernel/osrelease"); ok {
		s.Kernel = strings.TrimSpace(v)
	}
	if _, err := os.Stat(c.path("run/reboot-required")); err == nil {
		s.RebootRequired = true
	}

	zones, _ := filepath.Glob(c.path("sys/class/thermal/thermal_zone*/temp"))
	for _, z := range zones {
		b, err := os.ReadFile(z)
		if err != nil {
			continue
		}
		m, err := strconv.ParseFloat(strings.TrimSpace(string(b)), 64)
		if err != nil || m <= 0 {
			continue
		}
		if t := m / 1000; !s.TempKnown || t > s.TempC {
			s.TempC, s.TempKnown = t, true
		}
	}

	supplies, _ := filepath.Glob(c.path("sys/class/power_supply/*"))
	for _, d := range supplies {
		kind, _ := os.ReadFile(filepath.Join(d, "type"))
		if strings.TrimSpace(string(kind)) != "Battery" {
			continue
		}
		s.Power.Present = true
		st, _ := os.ReadFile(filepath.Join(d, "status"))
		s.Power.OnBattery = strings.TrimSpace(string(st)) == "Discharging"
		if b, err := os.ReadFile(filepath.Join(d, "capacity")); err == nil {
			s.Power.Percent, _ = strconv.Atoi(strings.TrimSpace(string(b)))
		}
	}
}
