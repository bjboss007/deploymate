package builder

import (
	"strings"
	"testing"
)

func TestMemoryAdvice(t *testing.T) {
	if got := MemoryAdvice(0); got != "" {
		t.Errorf("unknown memory should say nothing, got %q", got)
	}
	if got := MemoryAdvice(6 << 30); got != "" {
		t.Errorf("6 GiB is enough, got %q", got)
	}
	got := MemoryAdvice(3<<30 + 500<<20)
	for _, want := range []string{"3.5 GiB", "Prebuilt mode", "exit 137"} {
		if !strings.Contains(got, want) {
			t.Errorf("advice %q missing %q", got, want)
		}
	}
}
