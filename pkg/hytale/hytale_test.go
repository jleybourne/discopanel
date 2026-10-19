package hytale

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
)

func feedServer(lines ...string) DeviceFlow {
	var f DeviceFlow
	for _, l := range lines {
		sl := ParseServerLine(l)
		f.FeedServer(sl.Kind, sl.Value, sl.TTL)
	}
	return f
}

func TestDeviceFlowServerAuth(t *testing.T) {
	// Output of /auth login device as the server manual shows it, framed
	lines := []string{
		"[2026/01/17 12:00:01   INFO]   [AuthCommand] ===================================================================",
		"[2026/01/17 12:00:01   INFO]   [AuthCommand] DEVICE AUTHORIZATION",
		"[2026/01/17 12:00:01   INFO]   [AuthCommand] Visit: https://accounts.hytale.com/device",
		"[2026/01/17 12:00:01   INFO]   [AuthCommand] Enter code: ABCD-1234",
		"[2026/01/17 12:00:01   INFO]   [AuthCommand] Or visit: https://accounts.hytale.com/device?user_code=ABCD-1234",
		"[2026/01/17 12:00:01   INFO]   [AuthCommand] Waiting for authorization (expires in 900 seconds)...",
	}
	if ParseServerLine(lines[1]).Kind != LineDeviceHeader {
		t.Fatal("header not recognised")
	}
	f := feedServer(lines...)
	if f.Code != "ABCD-1234" {
		t.Errorf("code = %q", f.Code)
	}
	if f.URL != "https://accounts.hytale.com/device?user_code=ABCD-1234" {
		t.Errorf("url = %q, want the link carrying the code", f.URL)
	}
	if f.TTL() != 900*time.Second {
		t.Errorf("ttl = %v", f.TTL())
	}
	// Unframed output as the manual prints it parses the same
	if g := feedServer("Enter code: ABCD-1234", "Waiting for authorization (expires in 600 seconds)..."); g.Code != "ABCD-1234" || g.TTL() != 600*time.Second {
		t.Errorf("unframed = %+v", g)
	}
}

func TestDeviceFlowFirstCodeWins(t *testing.T) {
	f := feedServer(
		"Enter code: ABCD-1234",
		"Enter code: EVIL-0000",
		"Or visit: https://accounts.hytale.com/device?user_code=EVIL-0000",
		"Visit: https://accounts.hytale.com/device",
	)
	if f.Code != "ABCD-1234" || f.URL != "https://accounts.hytale.com/device" {
		t.Errorf("flow = %+v, a later code replaced the first", f)
	}
}

func TestServerLinesRejectChat(t *testing.T) {
	// Player and plugin text that echoes server messages must never count
	spoofed := []string{
		"[2026/01/17 12:00:01   INFO]   [Chat] <Mallory> use /auth login please",
		"<Mallory> https://accounts.hytale.com/device?user_code=EVIL-CODE",
		"[12:00:01 INFO] [Chat] Mallory: Visit: https://accounts.hytale.com/device?user_code=EVIL-CODE",
		"Mallory: Enter code: EVIL1234",
		"my code ABCD1234",
		"[INFO] [PlayerChat] Enter code: EVIL1234",
		"[INFO] [Broadcast] Authentication successful! Mode: OAUTH_DEVICE",
		"<Steve> Steve joined the server",
		"[INFO] [Chat] Steve joined the server",
		"<Mallory> authentication successful",
		"Mallory said: Server started on 0.0.0.0:5520",
		"[INFO] [SayCommand] DEVICE AUTHORIZATION",
		"<Mallory> No server tokens configured",
		"Visit: https://evil.example.com/device?user_code=EVIL-CODE",
		"Visit: https://accounts.hytale.com.evil.example/device",
	}
	for _, line := range spoofed {
		if sl := ParseServerLine(line); sl.Kind != LineOther {
			t.Errorf("%q parsed as %v %q", line, sl.Kind, sl.Value)
		}
	}
}

func TestDownloaderFlowVariants(t *testing.T) {
	tests := []struct {
		name  string
		lines []string
		url   string
		code  string
		ttl   time.Duration
	}{
		{
			name:  "downloader style link with query code",
			lines: []string{"Please visit the following URL to authenticate:", "https://oauth.accounts.hytale.com/oauth2/device/verify?user_code=qwer5678", "Authorization code: QWER5678", "This code expires in 15 minutes"},
			url:   "https://oauth.accounts.hytale.com/oauth2/device/verify?user_code=qwer5678",
			code:  "QWER5678",
			ttl:   15 * time.Minute,
		},
		{
			name:  "download links are not sign-in links",
			lines: []string{"downloading https://downloader.hytale.com/builds/server.zip", "checking code signature"},
		},
		{
			name:  "lowercase words after code are ignored",
			lines: []string{"the code expires soon"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var f DeviceFlow
			for _, l := range tt.lines {
				f.Feed(l)
			}
			if f.URL != tt.url || f.Code != tt.code {
				t.Errorf("got url %q code %q, want %q %q", f.URL, f.Code, tt.url, tt.code)
			}
			if tt.ttl != 0 && f.TTL() != tt.ttl {
				t.Errorf("ttl = %v, want %v", f.TTL(), tt.ttl)
			}
		})
	}
}

func TestParseServerLine(t *testing.T) {
	tests := []struct {
		line  string
		kind  LineKind
		value string
	}{
		{"Authentication successful! Mode: OAUTH_DEVICE", LineAuthSuccess, ""},
		{"[2026/01/17 12:00:00   WARN]   [ServerAuthManager] Token validation failed. Server starting unauthenticated.", LineUnauthenticated, ""},
		{"No server tokens configured", LineUnauthenticated, ""},
		{"2026-01-17 12:00:00.123 INFO [HytaleServer] Use /auth login to authenticate.", LineUnauthenticated, ""},
		{"Device code expired, run /auth login device again", LineAuthFailed, ""},
		{"Credential persistence set to Encrypted", LinePersistenceEncrypted, ""},
		{"[INFO] Server started on 0.0.0.0:5520", LineBooted, ""},
		{"[INFO] Starting server on 0.0.0.0:5520", LineOther, ""},
		{"[INFO] Kweebec_Kai (UUID: 1234abcd-0000) connected from 192.168.1.4", LinePlayerJoin, "Kweebec_Kai"},
		{"[INFO] Kweebec_Kai (UUID: 1234abcd-0000) disconnected", LinePlayerLeave, "Kweebec_Kai"},
		{"MossyBlade joined the server", LinePlayerJoin, "MossyBlade"},
		{"MossyBlade left the server", LinePlayerLeave, "MossyBlade"},
		{"Database connected", LineOther, ""},
		{"\x1b[32m[INFO] Server started\x1b[0m", LineBooted, ""},
	}
	for _, tt := range tests {
		sl := ParseServerLine(tt.line)
		if sl.Kind != tt.kind || sl.Value != tt.value {
			t.Errorf("%q: got %v %q, want %v %q", tt.line, sl.Kind, sl.Value, tt.kind, tt.value)
		}
	}
}

func TestParseVersion(t *testing.T) {
	tests := map[string]string{
		"2026.01.17-4b0f30090\n":                           "2026.01.17-4b0f30090",
		"checking for updates...\nversion: 2026.10.02-abc": "2026.10.02-abc",
		"\n\n1.4.2\n":  "1.4.2",
		"build-7":      "",
		"Unauthorized": "",
		"error: 401":   "",
		"Please visit https://accounts.hytale.com/device": "",
		"": "",
	}
	for in, want := range tests {
		if got := ParseVersion(in); got != want {
			t.Errorf("ParseVersion(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestObjectPreservesUnknownKeys(t *testing.T) {
	in := `{
  "Version": 3,
  "ServerName": "Old",
  "MaxPlayers": 100,
  "Defaults": {"World": "default", "GameMode": "Adventure"},
  "Update": {"Enabled": true, "Patchline": "release"},
  "Unknown": [1, 2, {"deep": null}]
}`
	o, err := ParseObject([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	if err := o.Set(KeyServerName, "Orbis Survival"); err != nil {
		t.Fatal(err)
	}
	if err := o.Set(KeyMaxViewRadius, 12); err != nil {
		t.Fatal(err)
	}
	out, err := o.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	back, err := ParseObject(out)
	if err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, out)
	}
	want := []string{"Version", "ServerName", "MaxPlayers", "Defaults", "Update", "Unknown", "MaxViewRadius"}
	if !slices.Equal(back.Keys(), want) {
		t.Errorf("keys = %v, want %v", back.Keys(), want)
	}
	cfg := ConfigFromObject(back)
	if cfg.ServerName != "Orbis Survival" || cfg.MaxPlayers != 100 || cfg.MaxViewRadius != 12 {
		t.Errorf("config = %+v", cfg)
	}
	var defaults map[string]string
	if !back.Get("Defaults", &defaults) || defaults["GameMode"] != "Adventure" {
		t.Errorf("nested object lost: %v", defaults)
	}
	if !strings.Contains(string(out), `"deep": null`) {
		t.Errorf("unknown nested value lost:\n%s", out)
	}
}

func TestParseObjectRejectsNonObjects(t *testing.T) {
	for _, in := range []string{`[]`, `"x"`, `{"a":1} {"b":2}`, `{"a":`} {
		if _, err := ParseObject([]byte(in)); err == nil {
			t.Errorf("%q accepted", in)
		}
	}
}

func TestApplyPending(t *testing.T) {
	dir, priv := t.TempDir(), t.TempDir()
	pending := NewObject()
	_ = pending.Set(KeyMaxPlayers, 40)
	if err := SavePending(priv, pending); err != nil {
		t.Fatal(err)
	}
	more := NewObject()
	_ = more.Set(KeyMOTD, "hi")
	if err := SavePending(priv, more); err != nil {
		t.Fatal(err)
	}
	on := true
	if err := SavePendingWhitelist(priv, &on, []string{"u1"}, nil); err != nil {
		t.Fatal(err)
	}
	if !HasPending(priv) {
		t.Fatal("pending edits not reported")
	}

	// Waits for the server's own config.json
	applied, err := ApplyPending(dir, priv)
	if err != nil || len(applied) != 0 {
		t.Fatalf("applied=%v err=%v before config.json exists", applied, err)
	}
	if _, err := os.Stat(filepath.Join(dir, ConfigFile)); !os.IsNotExist(err) {
		t.Fatal("panel wrote a partial config.json")
	}

	cfgPath := filepath.Join(dir, ConfigFile)
	if err := os.WriteFile(cfgPath, []byte(`{"Version":3,"MaxPlayers":100,"Mods":{}}`), 0600); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(dir, WhitelistFile), []byte(`{"enabled": false, "list": ["ingame"]}`), 0644)
	applied, err = ApplyPending(dir, priv)
	if err != nil || !slices.Equal(applied, []string{ConfigFile, WhitelistFile}) {
		t.Fatalf("applied=%v err=%v", applied, err)
	}
	o, err := ReadObjectFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if o.Int(KeyMaxPlayers) != 40 || o.Int("Version") != 3 || o.String(KeyMOTD) != "hi" {
		t.Errorf("merged config wrong: %v", o.Keys())
	}
	if info, _ := os.Stat(cfgPath); info.Mode().Perm() != 0600 {
		t.Errorf("mode = %v, want 0600 kept", info.Mode().Perm())
	}
	// The entry the server added while the change waited survives
	if w, _ := ReadWhitelist(dir); !w.Enabled || !slices.Equal(w.Entries, []string{"ingame", "u1"}) {
		t.Errorf("whitelist pending not applied as a delta: %+v", w)
	}
	if HasPending(priv) {
		t.Error("pending files not removed")
	}

	// An unrecognized whitelist keeps its pending edit
	_ = SavePendingWhitelist(priv, nil, []string{"u2"}, nil)
	_ = os.WriteFile(filepath.Join(dir, WhitelistFile), []byte(`[]`), 0644)
	if _, err := ApplyPending(dir, priv); err == nil || !HasPending(priv) {
		t.Errorf("unrecognized whitelist overwritten, err=%v", err)
	}
}

func TestSavePendingConcurrent(t *testing.T) {
	priv := t.TempDir()
	var wg sync.WaitGroup
	for i := range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			o := NewObject()
			_ = o.Set(fmt.Sprintf("K%d", i), i)
			if err := SavePending(priv, o); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	o, err := ReadPending(priv)
	if err != nil || o.Len() != 32 {
		t.Fatalf("lost edits: %d keys, %v", o.Len(), err)
	}
}

func TestApplyPendingKeepsEditSavedDuringApply(t *testing.T) {
	dir, priv := t.TempDir(), t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, ConfigFile), []byte(`{"MaxPlayers":1}`), 0644)
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(2)
		go func() {
			defer wg.Done()
			o := NewObject()
			_ = o.Set(fmt.Sprintf("K%d", i), i)
			_ = SavePending(priv, o)
		}()
		go func() {
			defer wg.Done()
			_, _ = ApplyPending(dir, priv)
		}()
	}
	wg.Wait()
	_, _ = ApplyPending(dir, priv)
	o, _ := ReadObjectFile(filepath.Join(dir, ConfigFile))
	if o.Len() != 21 {
		t.Errorf("config.json has %d keys, edits were dropped: %v", o.Len(), o.Keys())
	}
}

func TestWhitelistDeltas(t *testing.T) {
	priv := t.TempDir()
	_ = SavePendingWhitelist(priv, nil, []string{"a", "b"}, []string{"c"})
	_ = SavePendingWhitelist(priv, nil, []string{"C"}, []string{"a"})
	d, err := ReadPendingWhitelist(priv)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(d.Add, []string{"b", "C"}) || len(d.Remove) != 1 || d.Remove[0] != "a" {
		t.Errorf("delta = %+v", d)
	}
	if got := d.Apply([]string{"a", "x", "c"}); !slices.Equal(got, []string{"x", "c", "b"}) {
		t.Errorf("apply = %v", got)
	}
	add, remove := DiffWhitelist([]string{"a", "B"}, []string{"b", "c"})
	if !slices.Equal(add, []string{"c"}) || !slices.Equal(remove, []string{"a"}) {
		t.Errorf("diff = %v %v", add, remove)
	}
}

func TestValidateConfig(t *testing.T) {
	ok := ServerConfig{ServerName: "a", MaxPlayers: 100, MaxViewRadius: 12}
	if err := ValidateConfig(ok); err != nil {
		t.Errorf("valid config rejected: %v", err)
	}
	for _, bad := range []ServerConfig{
		{MaxPlayers: 0, MaxViewRadius: 12},
		{MaxPlayers: 10, MaxViewRadius: 0},
		{MaxPlayers: 10, MaxViewRadius: 500},
		{MaxPlayers: 10, MaxViewRadius: 12, Password: strings.Repeat("x", 200)},
	} {
		if err := ValidateConfig(bad); err == nil {
			t.Errorf("%+v accepted", bad)
		}
	}
}

func TestWhitelist(t *testing.T) {
	dir := t.TempDir()
	w, err := ReadWhitelist(dir)
	if err != nil || w.Format != v1.HytaleWhitelistFormat_HYTALE_WHITELIST_FORMAT_MISSING {
		t.Fatalf("missing file: %v %v", w.Format, err)
	}

	path := filepath.Join(dir, WhitelistFile)
	_ = os.WriteFile(path, []byte(`[{"uuid":"1"}]`), 0644)
	if w, _ = ReadWhitelist(dir); w.Format != v1.HytaleWhitelistFormat_HYTALE_WHITELIST_FORMAT_UNRECOGNIZED {
		t.Errorf("array shape read as %v", w.Format)
	}
	_ = os.WriteFile(path, []byte(`{"enabled": "yes", "list": []}`), 0644)
	if w, _ = ReadWhitelist(dir); w.Format != v1.HytaleWhitelistFormat_HYTALE_WHITELIST_FORMAT_UNRECOGNIZED {
		t.Errorf("string toggle read as %v", w.Format)
	}

	_ = os.WriteFile(path, []byte(`{"enabled": false, "list": ["a-uuid"], "extra": 1}`), 0644)
	w, err = ReadWhitelist(dir)
	if err != nil || w.Format != v1.HytaleWhitelistFormat_HYTALE_WHITELIST_FORMAT_RECOGNIZED || w.Enabled || len(w.Entries) != 1 {
		t.Fatalf("recognized shape read as %+v %v", w, err)
	}
	priv := t.TempDir()
	on := true
	entries, err := NormalizeWhitelist([]string{" a-uuid ", "", "b-uuid", "A-UUID"})
	if err != nil {
		t.Fatal(err)
	}
	add, remove := DiffWhitelist(w.Entries, entries)
	if err := SavePendingWhitelist(priv, &on, add, remove); err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyPending(dir, priv); err != nil {
		t.Fatal(err)
	}
	w, _ = ReadWhitelist(dir)
	if !w.Enabled || !slices.Equal(w.Entries, []string{"a-uuid", "b-uuid"}) {
		t.Errorf("after write: %+v", w)
	}
	o, _ := ReadObjectFile(path)
	if o.Int("extra") != 1 {
		t.Error("unknown whitelist key lost")
	}
	if _, err := NormalizeWhitelist([]string{`bad"name`}); err == nil {
		t.Error("quote accepted in entry")
	}
}

func TestSymlinkedServerFilesRefused(t *testing.T) {
	dir, outside := t.TempDir(), t.TempDir()
	secret := filepath.Join(outside, "host.json")
	_ = os.WriteFile(secret, []byte(`{"secret":"host"}`), 0644)
	if err := os.Symlink(secret, filepath.Join(dir, ConfigFile)); err != nil {
		t.Skip("symlinks unavailable:", err)
	}
	if _, err := ReadObjectFile(filepath.Join(dir, ConfigFile)); !errors.Is(err, ErrNotRegular) {
		t.Errorf("read through a symlink: %v", err)
	}
	if err := WriteFileAtomic(filepath.Join(dir, ConfigFile), []byte("{}"), 0644); !errors.Is(err, ErrNotRegular) {
		t.Errorf("write through a symlink: %v", err)
	}
	priv := t.TempDir()
	p := NewObject()
	_ = p.Set(KeyMaxPlayers, 2)
	_ = SavePending(priv, p)
	if _, err := ApplyPending(dir, priv); err == nil {
		t.Error("pending applied through a symlinked config.json")
	}
	if b, _ := os.ReadFile(secret); string(b) != `{"secret":"host"}` {
		t.Errorf("host file changed: %s", b)
	}
	// A linked parent dir is refused for writes too
	linkedDir := filepath.Join(dir, "linked")
	_ = os.Symlink(outside, linkedDir)
	if err := WriteFileAtomic(filepath.Join(linkedDir, "x.json"), []byte("{}"), 0644); err == nil {
		t.Error("wrote through a symlinked directory")
	}
	if _, err := os.Stat(filepath.Join(outside, "x.json")); err == nil {
		t.Error("file landed outside the data dir")
	}
}

func TestModsRefuseSymlinks(t *testing.T) {
	dir, other := t.TempDir(), t.TempDir()
	_ = os.WriteFile(filepath.Join(other, "victim.jar"), []byte("host"), 0644)
	if err := os.Symlink(other, filepath.Join(dir, ModsDir)); err != nil {
		t.Skip("symlinks unavailable:", err)
	}
	if _, err := ListMods(dir); !errors.Is(err, ErrUnsafeModPath) {
		t.Errorf("list through linked mods dir: %v", err)
	}
	if err := DeleteMod(dir, "victim.jar", true); !errors.Is(err, ErrUnsafeModPath) {
		t.Errorf("delete through linked mods dir: %v", err)
	}
	if _, err := SetModEnabled(dir, "victim.jar", false); !errors.Is(err, ErrUnsafeModPath) {
		t.Errorf("move through linked mods dir: %v", err)
	}
	src := filepath.Join(t.TempDir(), "up.jar")
	_ = os.WriteFile(src, []byte("new"), 0644)
	if _, err := PlaceMod(dir, src, "new.jar"); !errors.Is(err, ErrUnsafeModPath) {
		t.Errorf("upload through linked mods dir: %v", err)
	}
	if _, err := os.Stat(filepath.Join(other, "victim.jar")); err != nil {
		t.Error("host file was moved or deleted")
	}

	// A file inside a real mods dir that links elsewhere is left alone
	dir2 := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir2, ModsDir), 0755)
	_ = os.Symlink(filepath.Join(other, "victim.jar"), filepath.Join(dir2, ModsDir, "link.jar"))
	if list, _ := ListMods(dir2); len(list) != 0 {
		t.Errorf("linked jar listed: %v", list)
	}
	if err := DeleteMod(dir2, "link.jar", true); !errors.Is(err, ErrUnsafeModPath) {
		t.Errorf("delete of a linked jar: %v", err)
	}
	if _, err := SetModEnabled(dir2, "link.jar", false); !errors.Is(err, ErrUnsafeModPath) {
		t.Errorf("toggle of a linked jar: %v", err)
	}
	if _, err := os.Stat(filepath.Join(other, "victim.jar")); err != nil {
		t.Error("link target removed")
	}
}

func TestMods(t *testing.T) {
	dir := t.TempDir()
	mods := filepath.Join(dir, ModsDir)
	_ = os.MkdirAll(mods, 0755)
	write := func(p, body string) {
		t.Helper()
		if err := os.WriteFile(p, []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(mods, "land-claims-1.4.0.jar"), "jar")
	write(filepath.Join(mods, "autumn-textures.zip"), "zipzip")
	write(filepath.Join(mods, "notes.txt"), "ignored")
	write(filepath.Join(mods, ".hidden.jar"), "ignored")

	list, err := ListMods(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].FileName != "autumn-textures.zip" || list[0].Kind != v1.HytaleModKind_HYTALE_MOD_KIND_ASSET_PACK || list[1].Kind != v1.HytaleModKind_HYTALE_MOD_KIND_PLUGIN || list[0].Size != 6 {
		t.Fatalf("list = %v", list)
	}

	m, err := SetModEnabled(dir, "land-claims-1.4.0.jar", false)
	if err != nil || m.Enabled {
		t.Fatalf("disable: %v %v", m, err)
	}
	if _, err := os.Stat(filepath.Join(dir, DisabledModsDir, "land-claims-1.4.0.jar")); err != nil {
		t.Fatal("disabled mod not in mods_disabled")
	}
	// A new upload with the same name must not shadow the disabled one
	src := filepath.Join(t.TempDir(), "upload.jar")
	write(src, "new")
	if _, err := PlaceMod(dir, src, "land-claims-1.4.0.jar"); err != ErrModExists {
		t.Errorf("twin upload err = %v", err)
	}
	// A twin left in mods/ by hand blocks re-enabling instead of overwriting
	write(filepath.Join(mods, "land-claims-1.4.0.jar"), "twin")
	if _, err := SetModEnabled(dir, "land-claims-1.4.0.jar", true); err == nil {
		t.Error("enable overwrote a twin")
	}
	if b, _ := os.ReadFile(filepath.Join(mods, "land-claims-1.4.0.jar")); string(b) != "twin" {
		t.Error("twin content changed")
	}
	_ = os.Remove(filepath.Join(mods, "land-claims-1.4.0.jar"))
	if m, err = SetModEnabled(dir, "land-claims-1.4.0.jar", true); err != nil || !m.Enabled {
		t.Fatalf("enable: %v %v", m, err)
	}

	placed, err := PlaceMod(dir, src, "essentials-0.9.2.jar")
	if err != nil || !placed.Enabled || placed.Size != 3 {
		t.Fatalf("place: %v %v", placed, err)
	}
	if err := DeleteMod(dir, "essentials-0.9.2.jar", true); err != nil {
		t.Fatal(err)
	}
	if err := DeleteMod(dir, "essentials-0.9.2.jar", true); err != ErrModNotFound {
		t.Errorf("second delete err = %v", err)
	}
	for _, bad := range []string{"../x.jar", "a/b.jar", "x.exe", ".x.jar", "", "x.jar\n", `..\x.zip`} {
		if _, err := PlaceMod(dir, src, bad); err != ErrInvalidModName {
			t.Errorf("place %q err = %v", bad, err)
		}
		if err := DeleteMod(dir, bad, true); err != ErrInvalidModName {
			t.Errorf("delete %q err = %v", bad, err)
		}
	}
	entries, _ := os.ReadDir(mods)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}
}

func TestLaunchExec(t *testing.T) {
	dir := t.TempDir()
	want := "-jar HytaleServer.jar --assets Assets.zip --bind 0.0.0.0:5520 --auth-mode authenticated"
	if got := LaunchExec(dir); got != want {
		t.Errorf("without aot: %q", got)
	}
	_ = os.WriteFile(filepath.Join(dir, AOTCache), []byte("x"), 0644)
	if got := LaunchExec(dir); got != "-XX:AOTCache=HytaleServer.aot "+want {
		t.Errorf("with aot: %q", got)
	}
}

func TestDownloaderBinary(t *testing.T) {
	names := []string{"QUICKSTART.md", "hytale-downloader-windows-amd64.exe", "hytale-downloader-linux-amd64", "hytale-downloader-linux-arm64"}
	if got, err := DownloaderBinary(names, "x86_64"); err != nil || got != "hytale-downloader-linux-amd64" {
		t.Errorf("amd64: %q %v", got, err)
	}
	if got, err := DownloaderBinary(names, "aarch64"); err != nil || got != "hytale-downloader-linux-arm64" {
		t.Errorf("arm64: %q %v", got, err)
	}
	if _, err := DownloaderBinary(names[:3], "arm64"); err == nil {
		t.Error("arm64 picked an amd64 build")
	}
	if got, err := DownloaderBinary([]string{"bin/hytale-downloader-linux"}, "amd64"); err != nil || got != "bin/hytale-downloader-linux" {
		t.Errorf("unlabelled: %q %v", got, err)
	}
}

func TestDeleteModTwinsByLocation(t *testing.T) {
	dir := t.TempDir()
	for _, d := range []string{ModsDir, DisabledModsDir} {
		_ = os.MkdirAll(filepath.Join(dir, d), 0755)
		_ = os.WriteFile(filepath.Join(dir, d, "x.jar"), []byte(d), 0644)
	}
	if err := DeleteMod(dir, "x.jar", false); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(dir, ModsDir, "x.jar")); err != nil || string(b) != ModsDir {
		t.Error("deleting the disabled twin removed the enabled one")
	}
	if _, err := os.Stat(filepath.Join(dir, DisabledModsDir, "x.jar")); !os.IsNotExist(err) {
		t.Error("disabled twin still there")
	}
	if err := DeleteMod(dir, "x.jar", false); err != ErrModNotFound {
		t.Errorf("second delete err = %v", err)
	}
}

func TestDownloaderArgs(t *testing.T) {
	got := DownloaderArgs(DownloaderOpts{Binary: "/hytale/d", CredentialsPath: "/hytale/c.json", Patchline: "pre-release", DownloadPath: "/hytale/x.zip"})
	want := []string{"/hytale/d", "-skip-update-check", "-credentials-path", "/hytale/c.json", "-patchline", "pre-release", "-download-path", "/hytale/x.zip"}
	if !slices.Equal(got, want) {
		t.Errorf("download args = %v", got)
	}
	got = DownloaderArgs(DownloaderOpts{Binary: "/hytale/d", CredentialsPath: "/hytale/c.json", Patchline: "release"})
	if !slices.Equal(got, []string{"/hytale/d", "-skip-update-check", "-credentials-path", "/hytale/c.json", "-print-version"}) {
		t.Errorf("version args = %v", got)
	}
}

func TestSettingsAndPaths(t *testing.T) {
	panel := t.TempDir()
	priv, err := PrivateDir(panel, "srv-1")
	if err != nil || priv != filepath.Join(panel, "hytale", "srv-1") {
		t.Fatalf("private dir %q %v", priv, err)
	}
	for _, bad := range []string{"", "..", "../x", "a/b", ".hidden"} {
		if _, err := PrivateDir(panel, bad); err == nil {
			t.Errorf("server id %q accepted", bad)
		}
	}
	s, err := ReadSettings(priv)
	if err != nil || s.Patchline != PatchlineRelease {
		t.Fatalf("defaults: %v %v", s, err)
	}
	s.Patchline = PatchlinePreRelease
	if err := WriteSettings(priv, s); err != nil {
		t.Fatal(err)
	}
	if s, _ = ReadSettings(priv); s.Patchline != PatchlinePreRelease {
		t.Errorf("patchline = %q", s.Patchline)
	}
	if info, _ := os.Stat(priv); info.Mode().Perm() != 0700 {
		t.Errorf("private dir mode %v", info.Mode().Perm())
	}
	if _, err := NormalizePatchline("nightly"); err == nil {
		t.Error("unknown channel accepted")
	}
	p, err := MountedPath(priv, "/hytale", CredentialsPath(priv))
	if err != nil || p != "/hytale/.hytale-downloader-credentials.json" {
		t.Errorf("mounted path %q %v", p, err)
	}
	if _, err := MountedPath(priv, "/hytale", filepath.Dir(priv)); err == nil {
		t.Error("path outside the mount accepted")
	}
	if DownloaderSignedIn(priv) {
		t.Error("signed in without credentials")
	}

	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, UniverseDir, "worlds", "default"), 0755)
	_ = os.MkdirAll(filepath.Join(dir, DisabledModsDir), 0755)
	_ = os.WriteFile(filepath.Join(dir, ConfigFile), []byte("{}"), 0644)
	if got := BackupPaths(dir); !slices.Equal(got, []string{UniverseDir, DisabledModsDir, ConfigFile}) {
		t.Errorf("backup paths = %v", got)
	}
	if dirs, err := WorldDirs(dir); err != nil || len(dirs) != 1 {
		t.Errorf("world dirs = %v %v", dirs, err)
	}
	if !IsSecretFile("auth.enc") || !IsSecretFile("./auth.key") || IsSecretFile("universe/auth.enc") {
		t.Error("secret file detection")
	}
}
