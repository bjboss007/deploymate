package hostinfo

import (
	"fmt"
	"time"
)

// Level is how worried a finding should make you.
type Level int

const (
	OK Level = iota
	Warn
	Crit
)

func (l Level) String() string {
	switch l {
	case Warn:
		return "warning"
	case Crit:
		return "critical"
	}
	return "ok"
}

// Thresholds, in one place so the page, the API and the alerts agree.
const (
	DiskWarnFreePct = 15.0
	DiskCritFreePct = 5.0
	MemWarnAvailPct = 10.0
	MemCritAvailPct = 4.0
	PressureWarn    = 10.0 // % of time stalled on memory
	LoadWarnFactor  = 1.5  // load average per core
	CPUWarnPct      = 90.0
	TempWarnC       = 80.0
	TempCritC       = 90.0
	BatteryWarnPct  = 20
)

// Platform is what the Docker side of the server reports, supplied by the caller.
type Platform struct {
	DockerChecked  bool
	DockerUp       bool
	TraefikChecked bool
	TraefikUp      bool
}

// Finding is one thing worth saying about the host.
type Finding struct {
	Area    string // cpu, memory, disk, power, temperature, docker, traefik, machine
	Level   Level
	Message string
}

// Verdict is the overall state and the findings behind it (OK findings
// are not listed; an empty list with Level OK means all clear).
type Verdict struct {
	Level    Level
	Headline string
	Findings []Finding
}

// Evaluate turns a snapshot into a verdict.
func Evaluate(s Snapshot, p Platform) Verdict {
	var fs []Finding
	add := func(area string, l Level, format string, a ...any) {
		fs = append(fs, Finding{Area: area, Level: l, Message: fmt.Sprintf(format, a...)})
	}

	if p.DockerChecked && !p.DockerUp {
		add("docker", Crit, "The Docker engine is not answering, so apps cannot be built or started.")
	}
	if p.TraefikChecked && !p.TraefikUp {
		add("traefik", Crit, "Traefik is not running, so no domain is being served.")
	}

	if s.Supported {
		for _, d := range s.Disks {
			if d.Total == 0 {
				continue
			}
			free := 100 - d.UsedPct()
			switch {
			case free < DiskCritFreePct:
				add("disk", Crit, "%s is %.0f%% full (%s free). Builds and databases will start failing.", d.Label, d.UsedPct(), bytes(d.Free))
			case free < DiskWarnFreePct:
				add("disk", Warn, "%s is %.0f%% full (%s free).", d.Label, d.UsedPct(), bytes(d.Free))
			}
		}
		if s.MemTotal > 0 {
			avail := 100 - s.MemUsedPct()
			switch {
			case avail < MemCritAvailPct:
				add("memory", Crit, "Only %s of memory is available.", bytes(s.MemAvailable))
			case avail < MemWarnAvailPct:
				add("memory", Warn, "Memory is tight: %s available of %s.", bytes(s.MemAvailable), bytes(s.MemTotal))
			}
		}
		if s.PressureKnown && s.MemPressure >= PressureWarn {
			add("memory", Warn, "Processes are waiting on memory %.0f%% of the time (swapping or reclaiming).", s.MemPressure)
		}
		if s.Cores > 0 && s.Load5 > LoadWarnFactor*float64(s.Cores) {
			add("cpu", Warn, "Load is %.1f on %d cores: more work is waiting than the CPU can run.", s.Load5, s.Cores)
		} else if s.CPUKnown && s.CPUPct >= CPUWarnPct {
			add("cpu", Warn, "CPU is at %.0f%%.", s.CPUPct)
		}
		if s.TempKnown {
			switch {
			case s.TempC >= TempCritC:
				add("temperature", Crit, "The machine is at %.0f°C.", s.TempC)
			case s.TempC >= TempWarnC:
				add("temperature", Warn, "The machine is running hot: %.0f°C.", s.TempC)
			}
		}
		if s.Power.Present && s.Power.OnBattery {
			l := Warn
			if s.Power.Percent > 0 && s.Power.Percent < BatteryWarnPct {
				l = Crit
			}
			add("power", l, "Running on battery (%d%%). Check the power cable.", s.Power.Percent)
		}
		if s.RebootRequired {
			add("machine", Warn, "The operating system wants a reboot to finish an update.")
		}
	}

	v := Verdict{Findings: fs}
	for _, f := range fs {
		if f.Level > v.Level {
			v.Level = f.Level
		}
	}
	switch {
	case !s.Supported && len(fs) == 0:
		v.Headline = "Host details need Linux"
	case len(fs) == 0:
		v.Headline = "Healthy"
	default:
		// Lead with the worst finding.
		worst := fs[0]
		for _, f := range fs {
			if f.Level > worst.Level {
				worst = f
			}
		}
		v.Headline = worst.Message
	}
	return v
}

func bytes(n uint64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := uint64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

// FormatUptime renders an uptime such as "3 days 4 h".
func FormatUptime(d time.Duration) string {
	if d <= 0 {
		return "unknown"
	}
	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	switch {
	case days > 0:
		return fmt.Sprintf("%d d %d h", days, hours)
	case hours > 0:
		return fmt.Sprintf("%d h %d min", hours, int(d.Minutes())%60)
	}
	return fmt.Sprintf("%d min", int(d.Minutes()))
}
