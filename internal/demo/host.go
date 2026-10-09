package demo

import (
	"context"
	"time"

	"github.com/habibmuhammad/deploymate/internal/hostinfo"
)

// Host is the fictional server behind the demo instance: a healthy mini PC.
// Nothing here comes from a real machine.
type Host struct{}

// Snapshot returns a fixed, plausible reading.
func (Host) Snapshot(context.Context) hostinfo.Snapshot {
	return hostinfo.Snapshot{
		Supported: true, Time: time.Now(),
		Cores: 4, CPUKnown: true, CPUPct: 23, Load1: 0.62, Load5: 0.71, Load15: 0.58,
		MemTotal: 16 << 30, MemAvailable: 9<<30 + 400<<20, MemCached: 4 << 30,
		SwapTotal: 4 << 30, SwapUsed: 120 << 20,
		MemPressure: 0.4, PressureKnown: true,
		Disks: []hostinfo.Disk{
			{Label: "Data", Path: "/var/lib/deploymate", Total: 468 << 30, Free: 291 << 30},
		},
		NetRxBps: 184 << 10, NetTxBps: 62 << 10, NetKnown: true,
		Uptime: 19*24*time.Hour + 6*time.Hour,
		OS:     "Ubuntu 24.04 LTS", Kernel: "6.8.0-48-generic",
		TempC: 47, TempKnown: true,
	}
}
