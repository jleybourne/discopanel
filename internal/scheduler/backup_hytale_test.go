package scheduler

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
)

func TestResolveBackupPathsHytale(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := resolveBackupPaths(dir, v1.ModLoader_MOD_LOADER_HYTALE, nil); err == nil {
		t.Error("backup of a server without universe/ succeeded")
	}
	for _, p := range []string{"universe/worlds/default", "mods", "logs"} {
		if err := os.MkdirAll(filepath.Join(dir, p), 0755); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []string{"config.json", "whitelist.json", "HytaleServer.jar"} {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("{}"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	paths, missing, err := resolveBackupPaths(dir, v1.ModLoader_MOD_LOADER_HYTALE, nil)
	if err != nil || len(missing) != 0 {
		t.Fatal(err, missing)
	}
	if !slices.Equal(paths, []string{"universe", "mods", "config.json", "whitelist.json"}) {
		t.Errorf("paths = %v", paths)
	}

	// Explicit paths work the same as for Minecraft
	paths, _, err = resolveBackupPaths(dir, v1.ModLoader_MOD_LOADER_HYTALE, []string{"logs"})
	if err != nil || !slices.Equal(paths, []string{"logs"}) {
		t.Errorf("explicit = %v %v", paths, err)
	}

	// Minecraft default still wants level.dat
	if _, _, err := resolveBackupPaths(dir, v1.ModLoader_MOD_LOADER_VANILLA, nil); err == nil {
		t.Error("minecraft default backed up a Hytale layout")
	}
}
