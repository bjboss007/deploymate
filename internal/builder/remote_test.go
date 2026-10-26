package builder

import (
	"errors"
	"strings"
	"testing"
)

func TestSetRemoteHostOnlyAcceptsATCPAddress(t *testing.T) {
	t.Cleanup(func() { remoteHost = "" })
	for _, ok := range []string{"", "tcp://100.64.0.5:1234", "tcp://friend-pc.tail1234.ts.net:1234"} {
		if err := SetRemoteHost(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"100.64.0.5:1234", "ssh://u@h", "tcp://u:p@h:1", "tcp://h:1/path", "tcp://h", "tcp://h:1 --privileged", "http://h:1"} {
		if err := SetRemoteHost(bad); err == nil {
			t.Errorf("%q must be refused: the value ends up in a command line", bad)
		}
	}
}

func TestAnUnreachableHostFallsBackToThisServerAndSaysSo(t *testing.T) {
	t.Cleanup(func() { remoteHost = "" })
	oldDial := dialable
	t.Cleanup(func() { dialable = oldDial })
	var logs []string
	log := func(l string) { logs = append(logs, l) }

	if h := activeHost(log); h != "" || len(logs) != 0 {
		t.Fatalf("no host configured: %q %v", h, logs)
	}
	_ = SetRemoteHost("tcp://100.64.0.5:1234")
	dialable = func(string) error { return nil }
	if h := activeHost(log); h != "tcp://100.64.0.5:1234" || !strings.Contains(logs[0], "building on tcp://100.64.0.5:1234") {
		t.Fatalf("reachable: %q %v", h, logs)
	}
	logs = nil
	dialable = func(string) error { return errors.New("i/o timeout") }
	if h := activeHost(log); h != "" || len(logs) != 1 || !strings.Contains(logs[0], "building on this server instead") {
		t.Fatalf("unreachable must fall back, not fail the deploy: %q %v", h, logs)
	}
}
