package store

import (
	"path/filepath"
	"testing"
	"time"
)

func TestHostMetricsListAndPrune(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "dm.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	now := time.Now().UTC()
	for _, ago := range []time.Duration{10 * 24 * time.Hour, 2 * time.Hour, time.Minute} {
		if err := st.InsertHostMetric(HostMetric{TS: now.Add(-ago).Format(time.RFC3339Nano), CPU: 5, TempC: -1}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := st.ListHostMetrics(now.Add(-24 * time.Hour))
	if err != nil || len(got) != 2 || got[0].TS > got[1].TS {
		t.Fatalf("list: %v %+v", err, got)
	}
	n, err := st.PruneHostMetricsBefore(now.Add(-7 * 24 * time.Hour))
	if err != nil || n != 1 {
		t.Fatalf("prune removed %d (%v), want 1", n, err)
	}
}
