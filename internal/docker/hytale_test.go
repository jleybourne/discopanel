package docker

import (
	"testing"

	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
)

func TestDesiredImageHytale(t *testing.T) {
	c := &Client{}
	got, err := c.DesiredImage(&v1.Server{ModLoader: v1.ModLoader_MOD_LOADER_HYTALE})
	if err != nil || got != DefaultRuntimeImage+":java25" {
		t.Errorf("hytale image = %q, %v", got, err)
	}
	// A pinned Java 25 variant still wins
	got, _ = c.DesiredImage(&v1.Server{ModLoader: v1.ModLoader_MOD_LOADER_HYTALE, DockerImage: "java25-graal"})
	if got != DefaultRuntimeImage+":java25-graal" {
		t.Errorf("pinned hytale image = %q", got)
	}
	// Minecraft keeps resolving from its Java version
	got, _ = c.DesiredImage(&v1.Server{ModLoader: v1.ModLoader_MOD_LOADER_PAPER, JavaVersion: 21})
	if got != DefaultRuntimeImage+":java21" {
		t.Errorf("minecraft image = %q", got)
	}
}

func TestConfigHashSeparatesGames(t *testing.T) {
	c := &Client{}
	cfg := &v1.ServerProperties{}
	mc := &v1.Server{ModLoader: v1.ModLoader_MOD_LOADER_VANILLA, Port: 5520, Memory: 4096, DataPath: "/srv/x"}
	hy := &v1.Server{ModLoader: v1.ModLoader_MOD_LOADER_HYTALE, Port: 5520, Memory: 4096, DataPath: "/srv/x"}
	if c.DesiredConfigHash(mc, cfg) == c.DesiredConfigHash(hy, cfg) {
		t.Error("udp and tcp bindings share a config hash")
	}
	paper := &v1.Server{ModLoader: v1.ModLoader_MOD_LOADER_PAPER, Port: 5520, Memory: 4096, DataPath: "/srv/x"}
	if c.DesiredConfigHash(mc, cfg) != c.DesiredConfigHash(paper, cfg) {
		t.Error("minecraft loaders now change the config hash")
	}
}
