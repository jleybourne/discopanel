package services

import (
	"context"
	"path/filepath"
	"slices"
	"testing"

	"connectrpc.com/connect"
	"github.com/discohaus/discopanel/internal/auth"
	storage "github.com/discohaus/discopanel/internal/db"
	"github.com/discohaus/discopanel/internal/hytaletracker"
	"github.com/discohaus/discopanel/internal/lifecycle"
	"github.com/discohaus/discopanel/internal/metrics"
	"github.com/discohaus/discopanel/internal/rbac"
	"github.com/discohaus/discopanel/pkg/config"
	"github.com/discohaus/discopanel/pkg/hytale"
	"github.com/discohaus/discopanel/pkg/logger"
	optionsv1 "github.com/discohaus/discopanel/pkg/proto/discopanel/options/v1"
	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
)

func TestRestoreHytaleSwapsUniverse(t *testing.T) {
	data := t.TempDir()
	writeTree(t, data, map[string]string{
		"universe/worlds/default/chunks/0.bin": "current",
		"universe/worlds/default/new-after":    "made-after-backup",
		"universe/players/kai.json":            "current-player",
		"mods/claims.jar":                      "current-plugin",
		"mods/added-later.jar":                 "gone, mods swap as a unit",
		"mods_disabled/claims.jar":             "disabled after the backup",
		"config.json":                          `{"MaxPlayers":40}`,
		".discopanel/manifest.json":            "internal",
	})
	archive := zipTree(t, map[string]string{
		"universe/worlds/default/chunks/0.bin": "backup",
		"universe/players/kai.json":            "backup-player",
		"mods/claims.jar":                      "backup-plugin",
		"mods_disabled/old.jar":                "backup-disabled",
		"config.json":                          `{"MaxPlayers":20}`,
	})

	if err := restoreHytaleArchive(context.Background(), archive, data); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"universe/worlds/default/chunks/0.bin": "backup",
		"universe/players/kai.json":            "backup-player",
		"mods/claims.jar":                      "backup-plugin",
		"mods_disabled/old.jar":                "backup-disabled",
		"config.json":                          `{"MaxPlayers":20}`,
		".discopanel/manifest.json":            "internal",
	}
	for rel, body := range want {
		if got, ok := readFile(t, filepath.Join(data, rel)); !ok || got != body {
			t.Errorf("%s = %q (present %v), want %q", rel, got, ok, body)
		}
	}
	for _, gone := range []string{"universe/worlds/default/new-after", "mods/added-later.jar", "mods_disabled/claims.jar"} {
		if _, ok := readFile(t, filepath.Join(data, gone)); ok {
			t.Errorf("%s survived, the folder was merged, not replaced", gone)
		}
	}
}

func TestRestoreMinecraftIgnoresUniverseName(t *testing.T) {
	// A Minecraft data dir that happens to hold universe/ keeps merge behaviour
	data := t.TempDir()
	writeTree(t, data, map[string]string{
		"universe/a.txt": "current",
		"universe/b.txt": "kept",
	})
	archive := zipTree(t, map[string]string{"universe/a.txt": "backup"})
	if err := restoreArchiveExact(context.Background(), archive, data); err != nil {
		t.Fatal(err)
	}
	if got, _ := readFile(t, filepath.Join(data, "universe/b.txt")); got != "kept" {
		t.Error("minecraft restore swapped a non-world directory")
	}
}

func TestValidateHytaleUpdate(t *testing.T) {
	hy := &v1.Server{ModLoader: v1.ModLoader_MOD_LOADER_HYTALE, McVersion: "2026.10.02-abc"}
	mc := &v1.Server{ModLoader: v1.ModLoader_MOD_LOADER_PAPER, McVersion: "1.21.1"}
	tests := []struct {
		name   string
		server *v1.Server
		msg    *v1.UpdateServerRequest
		ok     bool
	}{
		{"hytale rename", hy, &v1.UpdateServerRequest{Name: "x", McVersion: hy.McVersion}, true},
		{"hytale java 25 image", hy, &v1.UpdateServerRequest{DockerImage: "java25-graal"}, true},
		{"hytale java 21 image", hy, &v1.UpdateServerRequest{DockerImage: "java21"}, false},
		{"hytale version pin", hy, &v1.UpdateServerRequest{McVersion: "1.21.1"}, false},
		{"hytale modpack", hy, &v1.UpdateServerRequest{ModpackId: "p"}, false},
		{"hytale low memory", hy, &v1.UpdateServerRequest{Memory: 2048}, false},
		{"hytale to minecraft", hy, &v1.UpdateServerRequest{ModLoader: v1.ModLoader_MOD_LOADER_PAPER}, false},
		{"minecraft to hytale", mc, &v1.UpdateServerRequest{ModLoader: v1.ModLoader_MOD_LOADER_HYTALE}, false},
		{"minecraft loader change", mc, &v1.UpdateServerRequest{ModLoader: v1.ModLoader_MOD_LOADER_FABRIC, McVersion: "1.20.1", Memory: 1024}, true},
		{"hytale proxied extra port", hy, &v1.UpdateServerRequest{AdditionalPorts: []*v1.NetworkPort{{HostPort: 1, ContainerPort: 1, ProxyEnabled: true}}}, false},
	}
	for _, tt := range tests {
		err := validateHytaleUpdate(tt.server, tt.msg)
		if (err == nil) != tt.ok {
			t.Errorf("%s: err = %v", tt.name, err)
		}
		if err != nil && connect.CodeOf(err) == connect.CodeUnknown {
			t.Errorf("%s: untyped error %v", tt.name, err)
		}
	}
}

func TestHytalePortRequests(t *testing.T) {
	var protos []v1.ModuleProtocol
	for _, r := range hytalePortRequests(5520, nil, false) {
		if r.Port != 5520 {
			t.Errorf("port %d", r.Port)
		}
		protos = append(protos, r.Protocol)
	}
	if !slices.Contains(protos, v1.ModuleProtocol_MODULE_PROTOCOL_TCP) || !slices.Contains(protos, v1.ModuleProtocol_MODULE_PROTOCOL_UDP) {
		t.Errorf("claim does not hold tcp and udp: %v", protos)
	}
}

func TestValidateHytaleExtraPorts(t *testing.T) {
	ok := []*v1.NetworkPort{
		{HostPort: 8080, ContainerPort: 8080, Protocol: v1.ModuleProtocol_MODULE_PROTOCOL_TCP},
		{HostPort: 5521, ContainerPort: 5520, Protocol: v1.ModuleProtocol_MODULE_PROTOCOL_TCP},
	}
	if err := validateHytaleExtraPorts(ok); err != nil {
		t.Errorf("valid ports refused: %v", err)
	}
	for _, bad := range []*v1.NetworkPort{
		{HostPort: 6000, ContainerPort: 5520, Protocol: v1.ModuleProtocol_MODULE_PROTOCOL_UDP},
		{HostPort: 6000, ContainerPort: 5520},
		{HostPort: 6000, ContainerPort: 7000, ProxyEnabled: true},
	} {
		if err := validateHytaleExtraPorts([]*v1.NetworkPort{bad}); connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Errorf("%+v accepted: %v", bad, err)
		}
	}
}

func TestReadHytaleConfigOverlaysPending(t *testing.T) {
	data, priv := t.TempDir(), t.TempDir()
	server := &v1.Server{Name: "Orbis", MaxPlayers: 100, DataPath: data, ModLoader: v1.ModLoader_MOD_LOADER_HYTALE}

	// Before first boot the panel shows its create-time values
	pending := hytale.NewObject()
	_ = pending.Set(hytale.KeyMaxViewRadius, 12)
	if err := hytale.SavePending(priv, pending); err != nil {
		t.Fatal(err)
	}
	cfg, err := readHytaleConfig(server, priv)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ConfigExists || cfg.ServerName != "Orbis" || cfg.MaxPlayers != 100 || cfg.MaxViewRadius != 12 || !cfg.PendingRestart || cfg.Patchline != hytale.PatchlineRelease {
		t.Errorf("before boot: %+v", cfg)
	}
	if cfg.WhitelistFormat != v1.HytaleWhitelistFormat_HYTALE_WHITELIST_FORMAT_MISSING {
		t.Errorf("whitelist format %v", cfg.WhitelistFormat)
	}

	writeTree(t, data, map[string]string{
		"config.json":    `{"ServerName":"From file","MaxPlayers":50,"MaxViewRadius":32,"Password":"p"}`,
		"whitelist.json": `{"enabled":true,"list":["u1"]}`,
	})
	cfg, err = readHytaleConfig(server, priv)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.ConfigExists || cfg.ServerName != "From file" || cfg.MaxPlayers != 50 || cfg.MaxViewRadius != 12 || cfg.Password != "p" {
		t.Errorf("after boot: %+v", cfg)
	}
	if !cfg.WhitelistEnabled || !slices.Equal(cfg.Whitelist, []string{"u1"}) {
		t.Errorf("whitelist: %+v", cfg)
	}
}

func hytaleRig(t *testing.T) (*HytaleService, *v1.Server, context.Context) {
	svc, server, ctx, _ := hytaleRigFull(t)
	return svc, server, ctx
}

func hytaleRigFull(t *testing.T) (*HytaleService, *v1.Server, context.Context, *storage.Store) {
	t.Helper()
	cfg := &config.Config{}
	cfg.Database.Path = filepath.Join(t.TempDir(), "hytale.db")
	cfg.Database.AutoMigrate = true
	store, err := storage.NewSQLiteStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	log := logger.New()
	server := &v1.Server{
		Id:         "h1",
		Name:       "Orbis",
		ModLoader:  v1.ModLoader_MOD_LOADER_HYTALE,
		Status:     v1.ServerStatus_SERVER_STATUS_STOPPED,
		Port:       5520,
		MaxPlayers: 100,
		Memory:     4096,
		DataPath:   t.TempDir(),
	}
	if err := store.CreateServer(context.Background(), server); err != nil {
		t.Fatal(err)
	}
	lm := lifecycle.NewManager(store, nil, nil, nil, nil, nil, cfg, nil, log)
	svc := NewHytaleService(store, nil, lm, hytaletracker.New(log), nil, nil, t.TempDir(), metrics.NewRecorder(store, log), log)
	ctx := auth.WithUser(context.Background(), &v1.User{Id: "u1"})
	return svc, server, ctx, store
}

// Enforcer with one role holding exactly perms
func limitedRole(t *testing.T, store *storage.Store, perms ...*v1.Permission) *rbac.Enforcer {
	t.Helper()
	enforcer, err := rbac.NewEnforcer(store.DB())
	if err != nil {
		t.Fatal(err)
	}
	if err := enforcer.SetPermissionsForRole("limited", perms); err != nil {
		t.Fatal(err)
	}
	return enforcer
}

func TestGetHytaleConfigRedactsPasswordWithoutFilesRead(t *testing.T) {
	svc, server, _, store := hytaleRigFull(t)
	writeTree(t, server.DataPath, map[string]string{"config.json": `{"Password":"hunter22"}`})
	props := &v1.Permission{Resource: optionsv1.ResourceType_RESOURCE_TYPE_SERVER_PROPERTIES, Action: optionsv1.ActionType_ACTION_TYPE_READ}
	svc.enforcer = limitedRole(t, store, props)
	ctx := auth.WithUser(context.Background(), &v1.User{Id: "u2", Roles: []string{"limited"}})
	res, err := svc.GetHytaleConfig(ctx, connect.NewRequest(&v1.GetHytaleConfigRequest{ServerId: server.Id}))
	if err != nil {
		t.Fatal(err)
	}
	if c := res.Msg.Config; c.Password != "" || !c.PasswordHidden || !c.PasswordSet {
		t.Errorf("password leaked or flags wrong: %+v", c)
	}
	files := &v1.Permission{Resource: optionsv1.ResourceType_RESOURCE_TYPE_FILES, Action: optionsv1.ActionType_ACTION_TYPE_READ}
	svc.enforcer = limitedRole(t, store, props, files)
	res, _ = svc.GetHytaleConfig(ctx, connect.NewRequest(&v1.GetHytaleConfigRequest{ServerId: server.Id}))
	if c := res.Msg.Config; c.Password != "hunter22" || c.PasswordHidden {
		t.Errorf("files reader got %+v", c)
	}
}

func TestDeleteHytaleModByLocation(t *testing.T) {
	svc, server, ctx := hytaleRig(t)
	writeTree(t, server.DataPath, map[string]string{"mods/x.jar": "on", "mods_disabled/x.jar": "off"})
	if _, err := svc.DeleteHytaleMod(ctx, connect.NewRequest(&v1.DeleteHytaleModRequest{ServerId: server.Id, FileName: "x.jar", Enabled: false})); err != nil {
		t.Fatal(err)
	}
	if got, ok := readFile(t, filepath.Join(server.DataPath, "mods/x.jar")); !ok || got != "on" {
		t.Error("enabled twin deleted")
	}
	if _, ok := readFile(t, filepath.Join(server.DataPath, "mods_disabled/x.jar")); ok {
		t.Error("disabled twin kept")
	}
}

func TestWhitelistSavedWhileRunningKeepsInGameChanges(t *testing.T) {
	svc, server, ctx := hytaleRig(t)
	writeTree(t, server.DataPath, map[string]string{
		"config.json":    `{}`,
		"whitelist.json": `{"enabled":true,"list":["a","b"]}`,
	})
	release, held := make(chan struct{}), make(chan struct{})
	go func() {
		_ = svc.lifecycle.Exclusive(context.Background(), server.Id, "start", false, func() error {
			close(held)
			<-release
			return nil
		})
	}()
	<-held
	// The operator drops b and adds c while the server runs
	if _, err := svc.UpdateHytaleConfig(ctx, connect.NewRequest(&v1.UpdateHytaleConfigRequest{
		ServerId: server.Id, UpdateWhitelist: true, Whitelist: []string{"a", "c"},
	})); err != nil {
		t.Fatal(err)
	}
	close(release)
	// Meanwhile the server added d in game
	writeTree(t, server.DataPath, map[string]string{"whitelist.json": `{"enabled":true,"list":["a","b","d"]}`})
	priv, _ := hytale.PrivateDir(svc.panelDataDir, server.Id)
	if _, err := hytale.ApplyPending(server.DataPath, priv); err != nil {
		t.Fatal(err)
	}
	w, _ := hytale.ReadWhitelist(server.DataPath)
	if !slices.Equal(w.Entries, []string{"a", "d", "c"}) {
		t.Errorf("entries = %v, want the in-game addition kept", w.Entries)
	}
}

func TestUpdateHytaleConfigStoppedWritesFile(t *testing.T) {
	svc, server, ctx := hytaleRig(t)
	writeTree(t, server.DataPath, map[string]string{
		"config.json": `{"Version":3,"ServerName":"Old","MaxPlayers":100,"MaxViewRadius":32,"Mods":{}}`,
	})
	radius, players := int32(12), int32(40)
	res, err := svc.UpdateHytaleConfig(ctx, connect.NewRequest(&v1.UpdateHytaleConfigRequest{
		ServerId:      server.Id,
		MaxViewRadius: &radius,
		MaxPlayers:    &players,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if res.Msg.Config.PendingRestart {
		t.Error("stopped server edit left pending")
	}
	got, _ := readFile(t, filepath.Join(server.DataPath, "config.json"))
	obj, err := hytale.ParseObject([]byte(got))
	if err != nil {
		t.Fatal(err)
	}
	if obj.Int(hytale.KeyMaxViewRadius) != 12 || obj.Int(hytale.KeyMaxPlayers) != 40 || obj.String(hytale.KeyServerName) != "Old" {
		t.Errorf("config.json = %s", got)
	}
	if !slices.Equal(obj.Keys(), []string{"Version", "ServerName", "MaxPlayers", "MaxViewRadius", "Mods"}) {
		t.Errorf("key order changed: %v", obj.Keys())
	}
	fresh, _ := svc.store.GetServer(ctx, server.Id)
	if fresh.MaxPlayers != 40 {
		t.Errorf("max players column = %d", fresh.MaxPlayers)
	}

	// Out of range values are refused before anything is written
	bad := int32(500)
	if _, err := svc.UpdateHytaleConfig(ctx, connect.NewRequest(&v1.UpdateHytaleConfigRequest{ServerId: server.Id, MaxViewRadius: &bad})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("bad radius err = %v", err)
	}
	// Whitelist edits need a whitelist.json the server wrote
	on := true
	if _, err := svc.UpdateHytaleConfig(ctx, connect.NewRequest(&v1.UpdateHytaleConfigRequest{ServerId: server.Id, WhitelistEnabled: &on})); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Errorf("missing whitelist err = %v", err)
	}
}

func TestUpdateHytaleConfigBusyServerStaysPending(t *testing.T) {
	svc, server, ctx := hytaleRig(t)
	writeTree(t, server.DataPath, map[string]string{"config.json": `{"MaxViewRadius":32}`})
	release := make(chan struct{})
	held := make(chan struct{})
	go func() {
		_ = svc.lifecycle.Exclusive(context.Background(), server.Id, "start", false, func() error {
			close(held)
			<-release
			return nil
		})
	}()
	<-held
	defer close(release)
	radius := int32(10)
	res, err := svc.UpdateHytaleConfig(ctx, connect.NewRequest(&v1.UpdateHytaleConfigRequest{ServerId: server.Id, MaxViewRadius: &radius}))
	if err != nil {
		t.Fatal(err)
	}
	if !res.Msg.Config.PendingRestart || res.Msg.Config.MaxViewRadius != 10 {
		t.Errorf("busy edit = %+v", res.Msg.Config)
	}
	if got, _ := readFile(t, filepath.Join(server.DataPath, "config.json")); got != `{"MaxViewRadius":32}` {
		t.Errorf("config.json written while a start ran: %s", got)
	}
}

func TestHytaleServiceRejectsMinecraft(t *testing.T) {
	svc, _, ctx := hytaleRig(t)
	mc := &v1.Server{Id: "mc", Name: "mc", ModLoader: v1.ModLoader_MOD_LOADER_PAPER, McVersion: "1.21.1", DataPath: t.TempDir(), Status: v1.ServerStatus_SERVER_STATUS_STOPPED}
	if err := svc.store.CreateServer(context.Background(), mc); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ListHytaleMods(ctx, connect.NewRequest(&v1.ListHytaleModsRequest{ServerId: "mc"})); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Errorf("minecraft server err = %v", err)
	}
	if _, err := getMinecraftServer(ctx, svc.store, "h1", "x"); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Errorf("minecraft rpc on hytale err = %v", err)
	}
	if _, err := getMinecraftServer(ctx, svc.store, "mc", "x"); err != nil {
		t.Errorf("minecraft rpc on minecraft err = %v", err)
	}
}
