package hostinfo

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func write(t *testing.T, root, rel, body string) {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func fixture(t *testing.T) string {
	root := t.TempDir()
	write(t, root, "proc/meminfo", "MemTotal: 8000000 kB\nMemFree: 1000000 kB\nMemAvailable: 4000000 kB\nBuffers: 100000 kB\nCached: 900000 kB\nSwapTotal: 2000000 kB\nSwapFree: 1500000 kB\n")
	write(t, root, "proc/stat", "cpu  100 0 100 800 0 0 0 0 0 0\ncpu0 1 1 1 1\n")
	write(t, root, "proc/loadavg", "0.50 1.25 2.00 1/200 999\n")
	write(t, root, "proc/pressure/memory", "some avg10=12.50 avg60=3.00 avg300=1.00 total=1\nfull avg10=0.00 avg60=0.00 avg300=0.00 total=0\n")
	write(t, root, "proc/net/dev", "Inter-|   Receive\n face |bytes packets errs drop fifo frame compressed multicast|bytes packets\n  eth0: 1000 1 0 0 0 0 0 0 2000 1 0 0 0 0 0 0\n    lo: 999999 1 0 0 0 0 0 0 999999 1 0 0 0 0 0 0\nveth1: 5000 1 0 0 0 0 0 0 5000 1 0 0 0 0 0 0\n")
	write(t, root, "proc/uptime", "93784.12 1000.00\n")
	write(t, root, "etc/os-release", "NAME=\"Ubuntu\"\nPRETTY_NAME=\"Ubuntu 24.04.2 LTS\"\n")
	write(t, root, "proc/sys/kernel/osrelease", "6.8.0-45-generic\n")
	write(t, root, "run/reboot-required", "")
	write(t, root, "sys/class/thermal/thermal_zone0/temp", "52000\n")
	write(t, root, "sys/class/thermal/thermal_zone1/temp", "71500\n")
	write(t, root, "sys/class/power_supply/BAT0/type", "Battery\n")
	write(t, root, "sys/class/power_supply/BAT0/status", "Discharging\n")
	write(t, root, "sys/class/power_supply/BAT0/capacity", "63\n")
	write(t, root, "sys/class/power_supply/AC/type", "Mains\n")
	return root
}

func TestSnapshotReadsTheFixture(t *testing.T) {
	root := fixture(t)
	c := &Collector{Root: root, Cores: 4, Paths: []DiskPath{{"Data", root}, {"Same device", root}}}
	s := c.Snapshot(context.Background())
	if !s.Supported {
		t.Fatal("fixture should be supported")
	}
	if s.MemTotal != 8000000*1024 || s.MemAvailable != 4000000*1024 || s.SwapUsed != 500000*1024 {
		t.Errorf("memory: %+v", s)
	}
	if got := s.MemUsedPct(); got < 49.9 || got > 50.1 {
		t.Errorf("used pct = %v, want 50", got)
	}
	if s.Load5 != 1.25 || s.Cores != 4 {
		t.Errorf("load/cores: %v %d", s.Load5, s.Cores)
	}
	if !s.PressureKnown || s.MemPressure != 12.5 {
		t.Errorf("pressure: %v %v", s.PressureKnown, s.MemPressure)
	}
	if s.OS != "Ubuntu 24.04.2 LTS" || s.Kernel != "6.8.0-45-generic" || !s.RebootRequired {
		t.Errorf("machine: %q %q %v", s.OS, s.Kernel, s.RebootRequired)
	}
	if s.Uptime < 26*time.Hour || s.Uptime > 27*time.Hour {
		t.Errorf("uptime = %v", s.Uptime)
	}
	if !s.TempKnown || s.TempC != 71.5 {
		t.Errorf("hottest zone = %v (known %v)", s.TempC, s.TempKnown)
	}
	if !s.Power.Present || !s.Power.OnBattery || s.Power.Percent != 63 {
		t.Errorf("power: %+v", s.Power)
	}
	if len(s.Disks) != 1 || s.Disks[0].Total == 0 {
		t.Errorf("disks (same device must be listed once): %+v", s.Disks)
	}
}

func TestCPUAndNetworkRatesComeFromTheDifferenceBetweenReadings(t *testing.T) {
	root := fixture(t)
	c := &Collector{Root: root}
	c.Snapshot(context.Background()) // primes the previous reading

	// 100 more busy ticks and 100 more idle: 50% busy. Network: +3000 rx on eth0 only.
	write(t, root, "proc/stat", "cpu  150 0 150 900 0 0 0 0 0 0\n")
	write(t, root, "proc/net/dev", "h\nh\n  eth0: 4000 1 0 0 0 0 0 0 2000 1 0 0 0 0 0 0\n    lo: 9 1 0 0 0 0 0 0 9 1 0 0 0 0 0 0\n")
	time.Sleep(20 * time.Millisecond)
	s := c.Snapshot(context.Background())
	if !s.CPUKnown || s.CPUPct < 49.9 || s.CPUPct > 50.1 {
		t.Errorf("cpu = %v (known %v), want 50", s.CPUPct, s.CPUKnown)
	}
	if !s.NetKnown || s.NetRxBps == 0 || s.NetTxBps != 0 {
		t.Errorf("net rx=%d tx=%d known=%v", s.NetRxBps, s.NetTxBps, s.NetKnown)
	}
}

func TestUnsupportedWithoutProc(t *testing.T) {
	s := (&Collector{Root: t.TempDir()}).Snapshot(context.Background())
	if s.Supported {
		t.Error("an empty root must be reported as unsupported")
	}
	v := Evaluate(s, Platform{})
	if v.Level != OK || !strings.Contains(v.Headline, "Linux") {
		t.Errorf("verdict: %+v", v)
	}
}

func snap() Snapshot {
	return Snapshot{
		Supported: true, Cores: 4, MemTotal: 8 << 30, MemAvailable: 4 << 30,
		Disks: []Disk{{Label: "Data", Total: 100 << 30, Free: 60 << 30}},
	}
}

func TestEvaluateThresholds(t *testing.T) {
	healthy := Evaluate(snap(), Platform{DockerChecked: true, DockerUp: true, TraefikChecked: true, TraefikUp: true})
	if healthy.Level != OK || healthy.Headline != "Healthy" {
		t.Fatalf("healthy host: %+v", healthy)
	}
	for _, c := range []struct {
		name string
		mut  func(*Snapshot)
		p    Platform
		want Level
		area string
	}{
		{"disk warn", func(s *Snapshot) { s.Disks[0].Free = 12 << 30 }, Platform{}, Warn, "disk"},
		{"disk crit", func(s *Snapshot) { s.Disks[0].Free = 3 << 30 }, Platform{}, Crit, "disk"},
		{"mem warn", func(s *Snapshot) { s.MemAvailable = 600 << 20 }, Platform{}, Warn, "memory"},
		{"mem crit", func(s *Snapshot) { s.MemAvailable = 100 << 20 }, Platform{}, Crit, "memory"},
		{"pressure", func(s *Snapshot) { s.PressureKnown, s.MemPressure = true, 25 }, Platform{}, Warn, "memory"},
		{"load", func(s *Snapshot) { s.Load5 = 9 }, Platform{}, Warn, "cpu"},
		{"cpu", func(s *Snapshot) { s.CPUKnown, s.CPUPct = true, 95 }, Platform{}, Warn, "cpu"},
		{"hot", func(s *Snapshot) { s.TempKnown, s.TempC = true, 85 }, Platform{}, Warn, "temperature"},
		{"very hot", func(s *Snapshot) { s.TempKnown, s.TempC = true, 95 }, Platform{}, Crit, "temperature"},
		{"battery", func(s *Snapshot) { s.Power = Power{Present: true, OnBattery: true, Percent: 80} }, Platform{}, Warn, "power"},
		{"battery low", func(s *Snapshot) { s.Power = Power{Present: true, OnBattery: true, Percent: 10} }, Platform{}, Crit, "power"},
		{"reboot", func(s *Snapshot) { s.RebootRequired = true }, Platform{}, Warn, "machine"},
		{"docker down", func(*Snapshot) {}, Platform{DockerChecked: true}, Crit, "docker"},
		{"traefik down", func(*Snapshot) {}, Platform{TraefikChecked: true}, Crit, "traefik"},
	} {
		s := snap()
		c.mut(&s)
		v := Evaluate(s, c.p)
		found := false
		for _, f := range v.Findings {
			found = found || (f.Area == c.area && f.Level == c.want)
		}
		if !found || v.Level != c.want {
			t.Errorf("%s: %+v", c.name, v)
		}
	}
	// A battery that is charging is not a finding.
	s := snap()
	s.Power = Power{Present: true, OnBattery: false, Percent: 50}
	if v := Evaluate(s, Platform{}); v.Level != OK {
		t.Errorf("charging battery flagged: %+v", v)
	}
}
