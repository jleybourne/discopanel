package services

import (
	"archive/zip"
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/discohaus/discopanel/internal/auth"
	"github.com/discohaus/discopanel/internal/hytaletracker"
	"github.com/discohaus/discopanel/pkg/config"
	"github.com/discohaus/discopanel/pkg/files"
	"github.com/discohaus/discopanel/pkg/hytale"
	"github.com/discohaus/discopanel/pkg/logger"
	optionsv1 "github.com/discohaus/discopanel/pkg/proto/discopanel/options/v1"
	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
	"google.golang.org/protobuf/proto"
)

func TestFileGuardPrivatePaths(t *testing.T) {
	data := t.TempDir()
	writeTree(t, data, map[string]string{
		".discopanel/agent.json": "token",
		"auth.enc":               "secret",
		"auth.key":               "key",
		"config.json":            "{}",
	})
	hy := &v1.Server{DataPath: data, ModLoader: v1.ModLoader_MOD_LOADER_HYTALE}
	mc := &v1.Server{DataPath: data, ModLoader: v1.ModLoader_MOD_LOADER_PAPER}
	s := &FileService{}
	cases := []struct {
		server *v1.Server
		rel    string
		read   bool
		denied bool
	}{
		{hy, ".discopanel/agent.json", true, true},
		{hy, ".discopanel", false, true},
		{mc, ".discopanel/agent.json", true, true},
		{hy, "auth.enc", true, true},
		{hy, "auth.key", true, true},
		{hy, "auth.enc", false, false}, // deleting signs the server out
		{mc, "auth.enc", true, false},  // only Hytale names these files
		{hy, "config.json", true, false},
	}
	for _, c := range cases {
		err := s.guardPath(c.server, filepath.Join(data, c.rel), c.read)
		if (err != nil) != c.denied {
			t.Errorf("%v %s read=%v: err=%v", c.server.ModLoader, c.rel, c.read, err)
		}
		if err != nil && connect.CodeOf(err) != connect.CodePermissionDenied {
			t.Errorf("code = %v", connect.CodeOf(err))
		}
	}

	// A link inside the data dir pointing at a private file is just as private
	if err := os.Symlink(filepath.Join(data, ".discopanel", "agent.json"), filepath.Join(data, "innocent.txt")); err != nil {
		t.Skip("symlinks unavailable:", err)
	}
	_ = os.Symlink(filepath.Join(data, "auth.enc"), filepath.Join(data, "notes.txt"))
	for _, name := range []string{"innocent.txt", "notes.txt"} {
		if err := s.guardPath(hy, filepath.Join(data, name), true); err == nil {
			t.Errorf("%s read through a link", name)
		}
	}

	// Archives leave private paths out, links included
	dest := filepath.Join(t.TempDir(), "a.zip")
	if _, err := files.CreateZipArchiveFiltered([]string{"."}, data, dest, true, s.archiveSkip(hy)); err != nil {
		t.Fatal(err)
	}
	zr, err := zip.OpenReader(dest)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	var names []string
	for _, f := range zr.File {
		names = append(names, f.Name)
	}
	for _, bad := range []string{".discopanel/agent.json", "auth.enc", "auth.key", "innocent.txt", "notes.txt"} {
		if slices.Contains(names, bad) {
			t.Errorf("%s archived: %v", bad, names)
		}
	}
	if !slices.Contains(names, "config.json") {
		t.Errorf("config.json missing: %v", names)
	}

	// Listings hide the panel dir and mark credentials read only
	list := visibleListing(hy, []*v1.FileInfo{{Path: ".discopanel", IsDir: true}, {Path: "auth.enc", IsEditable: true}, {Path: "config.json", IsEditable: true}})
	if len(list) != 2 || list[0].Path != "auth.enc" || list[0].IsEditable || !list[1].IsEditable {
		t.Errorf("listing = %+v", list)
	}
}

func TestSignInWithheldWithoutCommandPermission(t *testing.T) {
	_, server, _, store := hytaleRigFull(t)
	tracker := hytaletracker.New(logger.New())
	tracker.SetDownloaderSignIn(server.Id, hytale.DeviceFlow{URL: "https://accounts.hytale.com/device?user_code=ABCD-1234", Code: "ABCD-1234"}, time.Now())
	s := &ServerService{config: &config.Config{}, hytale: tracker}
	s.config.Storage.DataDir = t.TempDir()
	viewer := auth.WithUser(context.Background(), &v1.User{Id: "v", Roles: []string{"limited"}})

	s.enforcer = limitedRole(t, store, &v1.Permission{Resource: optionsv1.ResourceType_RESOURCE_TYPE_SERVERS, Action: optionsv1.ActionType_ACTION_TYPE_READ})
	got := proto.Clone(server).(*v1.Server)
	s.applyHytale(viewer, got)
	if si := got.Hytale.SignIn; si == nil || si.Code != "" || si.Url != "" || !si.Withheld {
		t.Errorf("viewer sign-in = %+v", si)
	}

	s.enforcer = limitedRole(t, store, &v1.Permission{Resource: optionsv1.ResourceType_RESOURCE_TYPE_SERVERS, Action: optionsv1.ActionType_ACTION_TYPE_COMMAND})
	got = proto.Clone(server).(*v1.Server)
	s.applyHytale(viewer, got)
	if si := got.Hytale.SignIn; si == nil || si.Code != "ABCD-1234" || si.Withheld {
		t.Errorf("operator sign-in = %+v", si)
	}
}

func TestZipSkipsPanelStateEverywhere(t *testing.T) {
	data := t.TempDir()
	writeTree(t, data, map[string]string{".discopanel/agent.json": "token", "world/level.dat": "x"})
	dest := filepath.Join(t.TempDir(), "b.zip")
	if _, err := files.CreateZipArchive([]string{".", ".discopanel"}, data, dest, true); err != nil {
		t.Fatal(err)
	}
	zr, err := zip.OpenReader(dest)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	for _, f := range zr.File {
		if files.IsPanelState(f.Name) {
			t.Errorf("%s archived", f.Name)
		}
	}
}
