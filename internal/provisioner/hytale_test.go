package provisioner

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/discohaus/discopanel/pkg/hytale"
)

func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, body := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read %s: %v", p, err)
	}
	return string(b)
}

func TestPlaceHytaleFiles(t *testing.T) {
	data := t.TempDir()
	writeTree(t, data, map[string]string{
		"HytaleServer.jar":          "old jar",
		"old-native.so":             "dropped by the new build",
		"config.json":               `{"ServerName":"mine"}`,
		"universe/worlds/default/x": "world",
		"mods/claims.jar":           "plugin",
		"user-notes.txt":            "not ours",
	})
	staging := t.TempDir()
	// Archive wraps everything in one folder
	writeTree(t, staging, map[string]string{
		"2026.10.02-abc/Assets.zip":              "assets",
		"2026.10.02-abc/Server/HytaleServer.jar": "new jar",
		"2026.10.02-abc/Server/HytaleServer.aot": "aot",
		"2026.10.02-abc/Server/Licenses/a.txt":   "license",
		"2026.10.02-abc/Server/config.json":      `{"ServerName":"default"}`,
		"2026.10.02-abc/start.sh":                "ignored",
	})

	placed, err := placeHytaleFiles(staging, data, []string{"HytaleServer.jar", "old-native.so", "../escape", "config.json"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"Assets.zip", "HytaleServer.aot", "HytaleServer.jar", "Licenses"}
	if !slices.Equal(placed, want) {
		t.Errorf("placed = %v, want %v", placed, want)
	}
	if got := readFile(t, filepath.Join(data, "HytaleServer.jar")); got != "new jar" {
		t.Errorf("jar = %q", got)
	}
	if got := readFile(t, filepath.Join(data, "config.json")); got != `{"ServerName":"mine"}` {
		t.Errorf("config.json overwritten: %q", got)
	}
	for _, kept := range []string{"universe/worlds/default/x", "mods/claims.jar", "user-notes.txt", "Licenses/a.txt"} {
		if _, err := os.Stat(filepath.Join(data, kept)); err != nil {
			t.Errorf("%s missing: %v", kept, err)
		}
	}
	if _, err := os.Stat(filepath.Join(data, "old-native.so")); !os.IsNotExist(err) {
		t.Error("stale file from the previous build kept")
	}
	if _, err := os.Stat(filepath.Join(data, "start.sh")); !os.IsNotExist(err) {
		t.Error("file outside Server/ placed")
	}
	if !hytale.Installed(data) {
		t.Error("install not detected")
	}
	if exec := hytale.LaunchExec(data); exec[:len("-XX:AOTCache=")] != "-XX:AOTCache=" {
		t.Errorf("aot cache not used: %q", exec)
	}
}

func TestPlaceHytaleFilesSeedsMissingDefaults(t *testing.T) {
	data := t.TempDir()
	staging := t.TempDir()
	writeTree(t, staging, map[string]string{
		"Assets.zip":              "assets",
		"Server/HytaleServer.jar": "jar",
		"Server/config.json":      "{}",
	})
	placed, err := placeHytaleFiles(staging, data, nil)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(placed, "config.json") {
		t.Error("server owned file recorded as an install file")
	}
	if _, err := os.Stat(filepath.Join(data, "config.json")); err != nil {
		t.Error("shipped default not seeded")
	}
}

func TestPlaceHytaleFilesRejectsBadArchives(t *testing.T) {
	for name, tree := range map[string]map[string]string{
		"no jar":    {"Assets.zip": "a", "Server/readme": "x"},
		"no assets": {"Server/HytaleServer.jar": "j"},
	} {
		staging := t.TempDir()
		writeTree(t, staging, tree)
		if _, err := placeHytaleFiles(staging, t.TempDir(), nil); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestFindDownloader(t *testing.T) {
	dir := t.TempDir()
	if _, err := findDownloader(dir, "x86_64"); err == nil {
		t.Error("empty dir yielded a downloader")
	}
	writeTree(t, dir, map[string]string{
		"hytale-downloader-linux-amd64":       "elf",
		"hytale-downloader-windows-amd64.exe": "pe",
		"QUICKSTART.md":                       "doc",
	})
	rel, err := findDownloader(dir, "x86_64")
	if err != nil || rel != filepath.Join(hytaleDownloaderDir, "hytale-downloader-linux-amd64") {
		t.Fatalf("rel = %q, %v", rel, err)
	}
	info, _ := os.Stat(filepath.Join(dir, "hytale-downloader-linux-amd64"))
	if info.Mode().Perm()&0111 == 0 {
		t.Error("downloader not executable")
	}
	if _, err := findDownloader(dir, "aarch64"); err == nil {
		t.Error("arm64 host got an amd64 build")
	}
}

func TestDownloaderArgsStayInPrivateMount(t *testing.T) {
	priv := t.TempDir()
	args, err := downloaderArgs(priv, filepath.Join(hytaleDownloaderDir, "hytale-downloader-linux-amd64"), "release", filepath.Join(priv, hytaleServerZip))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"/hytale/downloader/hytale-downloader-linux-amd64", "-skip-update-check", "-credentials-path", "/hytale/.hytale-downloader-credentials.json", "-download-path", "/hytale/server.zip"}
	if !slices.Equal(args, want) {
		t.Errorf("args = %v", args)
	}
	for _, a := range args {
		if strings.HasPrefix(a, "/data") {
			t.Errorf("downloader reaches the data dir: %s", a)
		}
	}
	if _, err := downloaderArgs(priv, "x", "release", filepath.Join(filepath.Dir(priv), "escape.zip")); err == nil {
		t.Error("download path outside the private dir accepted")
	}
}

func TestPlaceHytaleFilesFromAnotherDir(t *testing.T) {
	// The staging dir lives in the panel private dir, not under the data dir
	data, staging := t.TempDir(), t.TempDir()
	writeTree(t, staging, map[string]string{"Assets.zip": "a", "Server/HytaleServer.jar": "j"})
	if _, err := placeHytaleFiles(staging, data, nil); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, filepath.Join(data, "HytaleServer.jar")); got != "j" {
		t.Errorf("jar = %q", got)
	}
}
