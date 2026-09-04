package httpserver

import (
	"context"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/habibmuhammad/deploymate/web/templates"
)

// statsWindow is the fleet stats lookback, matching the per-app History
// page (handlers_history.go).
const statsWindow = 30 * 24 * time.Hour

// handleStatsPage renders the fleet-wide build statistics: totals across
// all apps, a per-app table, deploys per day, and the storage snapshot.
func (s *Server) handleStatsPage(w http.ResponseWriter, r *http.Request) {
	since := time.Now().UTC().Add(-statsWindow)

	stats, err := s.store.DeploymentStatsAll(since)
	if err != nil {
		slog.Error("stats: deployment stats", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	perApp, err := s.store.DeploymentStatsPerApp(since)
	if err != nil {
		slog.Error("stats: per-app stats", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	perDay, err := s.store.DeploysPerDay(since)
	if err != nil {
		slog.Error("stats: per-day stats", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	render(w, r, http.StatusOK, templates.StatsPage(s.viewCtx(r), templates.FleetStats{
		Total:       stats.Total,
		SuccessRate: pct(stats.Succeeded, stats.Total),
		AvgBuildSec: stats.AvgBuildSec,
	}, perApp, perDay, s.diskStats(r.Context())))
}

// diskStats assembles the storage panel: a live docker system df snapshot,
// the tracked app-images total from the store, and the per-volume +
// top-image tables. The panel is best-effort — a nil runtime (tests) hides
// it entirely, and a daemon error renders a warning note instead of taking
// the page down (docker's DiskUsage can be slow on Docker Desktop).
func (s *Server) diskStats(ctx context.Context) *templates.DiskStats {
	if s.rt == nil {
		return nil
	}
	du, err := s.rt.DiskUsage(ctx)
	if err != nil {
		slog.Warn("stats: disk usage", "err", err)
		return &templates.DiskStats{Error: "docker daemon unreachable — storage numbers unavailable"}
	}
	tracked, err := s.store.TotalImageBytes()
	if err != nil {
		slog.Warn("stats: total image bytes", "err", err)
	}
	out := &templates.DiskStats{
		ImagesBytes: du.ImagesBytes, ContainersBytes: du.ContainersBytes,
		VolumesBytes: du.VolumesBytes, BuildCacheBytes: du.BuildCacheBytes,
		ReclaimableBytes: du.ReclaimableBytes,
		ImageCount: du.ImageCount, ContainerCount: du.ContainerCount, VolumeCount: du.VolumeCount,
		TrackedImageBytes: tracked,
	}

	// Per-volume table: every daemon volume with a size, flagged when it
	// belongs to a DeployMate service (matched by the derived volume name).
	// Orphaned daemon volumes show as "not tracked" — that is the point of
	// the panel: finding what eats disk.
	trackedVolumes := make(map[string]bool)
	if svcs, err := s.store.ListAllServices(); err == nil {
		for _, sv := range svcs {
			trackedVolumes[sv.VolumeName] = true
		}
	}
	for _, v := range du.Volumes {
		out.Volumes = append(out.Volumes, templates.VolumeRow{Name: v.Name, Bytes: v.Size, Tracked: trackedVolumes[v.Name]})
	}
	sort.Slice(out.Volumes, func(i, j int) bool { return out.Volumes[i].Bytes > out.Volumes[j].Bytes })

	// Top images by on-disk size; dangling images render as <none>.
	sort.Slice(du.Images, func(i, j int) bool { return du.Images[i].Size > du.Images[j].Size })
	for _, img := range du.Images {
		if len(out.TopImages) == 5 {
			break
		}
		tag := strings.Join(img.Tags, ", ")
		if tag == "" {
			tag = "<none>"
		}
		out.TopImages = append(out.TopImages, templates.DiskImage{Tag: tag, Size: img.Size, UsedBy: img.UsedBy})
	}
	return out
}
