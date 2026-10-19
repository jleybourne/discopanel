package metrics

import (
	"testing"
	"time"

	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
)

type fakeProbe struct {
	booted bool
	count  int
	names  []string
	known  bool
}

func (f *fakeProbe) Booted(string) bool                       { return f.booted }
func (f *fakeProbe) ObserveContainer(string, time.Time, bool) {}
func (f *fakeProbe) PlayersOnline(string) (int, []string, bool) {
	return f.count, f.names, f.known
}

func TestCollectHytaleHealthAndPlayers(t *testing.T) {
	c := NewCollector(nil, nil, nil, nil, nil, DefaultConfig())
	probe := &fakeProbe{}
	c.SetHytaleProbe(probe)
	server := &v1.Server{Id: "h1", ContainerId: "c1", MaxPlayers: 100, ModLoader: v1.ModLoader_MOD_LOADER_HYTALE}
	c.setServerContainers([]*v1.Server{server})

	started := time.Now().Add(-time.Minute)
	c.collectHytale(server, started)
	if got := c.ContainerHealth("c1", started); got != v1.ServerStatus_SERVER_STATUS_STARTING {
		t.Errorf("before boot = %v", got)
	}
	// Past the grace window an unmatched boot line reads running, not unhealthy
	old := time.Now().Add(-time.Hour)
	c.collectHytale(server, old)
	if got := c.ContainerHealth("c1", old); got != v1.ServerStatus_SERVER_STATUS_UNSPECIFIED {
		t.Errorf("no boot line after grace = %v", got)
	}
	if m := c.GetMetrics("h1"); m.PlayersOnline != 0 || m.PlayerSample != nil {
		t.Errorf("unknown players reported: %+v", m)
	}

	probe.booted, probe.count, probe.names, probe.known = true, 2, []string{"a", "b"}, true
	c.collectHytale(server, started)
	if got := c.ContainerHealth("c1", started); got != v1.ServerStatus_SERVER_STATUS_RUNNING {
		t.Errorf("after boot = %v", got)
	}
	if m := c.GetMetrics("h1"); m.PlayersOnline != 2 || len(m.PlayerSample) != 2 || m.MaxPlayers != 100 {
		t.Errorf("players = %+v", m)
	}
}
