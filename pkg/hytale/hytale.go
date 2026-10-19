// Facts and file formats for hosting the official Hytale dedicated server
package hytale

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/discohaus/discopanel/pkg/files"
	"github.com/discohaus/discopanel/pkg/minecraft"
	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
	"google.golang.org/protobuf/encoding/protojson"
)

const (
	// QUIC over UDP, the server's default bind port
	DefaultPort = 5520

	// Port the server binds inside its container, the host port maps onto it
	ContainerPort = 5520

	// Smallest container memory the server is documented to run in
	MinMemoryMB = 4096

	// Java major the server requires
	JavaMajor = 25

	// Release channels the downloader accepts
	PatchlineRelease    = "release"
	PatchlinePreRelease = "pre-release"

	// Server files the downloader ships, placed in the data dir root
	ServerJar = "HytaleServer.jar"
	AOTCache  = "HytaleServer.aot"
	Assets    = "Assets.zip"

	// Worlds and player data, swapped as one unit on restore
	UniverseDir = "universe"

	// Plugins (.jar) and asset packs (.zip)
	ModsDir = "mods"
	// Disabled mods move here, the server never reads it
	DisabledModsDir = "mods_disabled"

	ConfigFile      = "config.json"
	WhitelistFile   = "whitelist.json"
	BansFile        = "bans.json"
	PermissionsFile = "permissions.json"

	// Server credentials written by /auth persistence Encrypted, never served by Files
	AuthEncFile = "auth.enc"
	AuthKeyFile = "auth.key"

	// Panel-only state lives outside the data dir the game container mounts
	privateRoot      = "hytale"
	settingsFileName = "hytale.json"
	credentialsName  = ".hytale-downloader-credentials.json"
)

// Paths the game container mounts and must never read through the panel
var SecretFiles = []string{AuthEncFile, AuthKeyFile}

// Normalizes a release channel, empty means release
func NormalizePatchline(p string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(p)) {
	case "", PatchlineRelease:
		return PatchlineRelease, nil
	case PatchlinePreRelease, "prerelease":
		return PatchlinePreRelease, nil
	}
	return "", fmt.Errorf("unknown release channel %q, use %q or %q", p, PatchlineRelease, PatchlinePreRelease)
}

// Panel-private dir for one server: downloader, its credentials, settings and
// pending edits. It sits under the panel data dir, never inside the server data
// dir, so the game container and the Files tab cannot reach it.
func PrivateDir(panelDataDir, serverID string) (string, error) {
	if serverID == "" || serverID != filepath.Base(serverID) || strings.HasPrefix(serverID, ".") {
		return "", fmt.Errorf("invalid server id %q", serverID)
	}
	return filepath.Join(panelDataDir, privateRoot, serverID), nil
}

// Downloader credentials inside a private dir
func CredentialsPath(privateDir string) string {
	return filepath.Join(privateDir, credentialsName)
}

// Host path as seen from a container that mounts root at mount
func MountedPath(root, mount, hostPath string) (string, error) {
	rel, err := filepath.Rel(root, hostPath)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%s is outside %s", hostPath, root)
	}
	if rel == "." {
		return mount, nil
	}
	return strings.TrimSuffix(mount, "/") + "/" + filepath.ToSlash(rel), nil
}

func settingsPath(privateDir string) string {
	return filepath.Join(privateDir, settingsFileName)
}

// Edits to a server file saved while the server ran, applied at next start
func PendingPath(privateDir, target string) string {
	return filepath.Join(privateDir, strings.TrimSuffix(target, ".json")+".pending.json")
}

// Loads panel Hytale settings, defaults when absent
func ReadSettings(privateDir string) (*v1.HytaleSettings, error) {
	data, err := os.ReadFile(settingsPath(privateDir))
	if os.IsNotExist(err) {
		return &v1.HytaleSettings{Version: 1, Patchline: PatchlineRelease}, nil
	}
	if err != nil {
		return nil, err
	}
	var s v1.HytaleSettings
	if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("invalid Hytale settings: %w", err)
	}
	if s.Patchline, err = NormalizePatchline(s.Patchline); err != nil {
		s.Patchline = PatchlineRelease
	}
	return &s, nil
}

// Persists panel Hytale settings atomically
func WriteSettings(privateDir string, s *v1.HytaleSettings) error {
	if s.Version == 0 {
		s.Version = 1
	}
	data, err := protojson.MarshalOptions{Multiline: true}.Marshal(s)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(privateDir, 0700); err != nil {
		return err
	}
	return WriteFileAtomic(settingsPath(privateDir), data, 0600)
}

// Reports whether a downloader sign-in was saved
func DownloaderSignedIn(privateDir string) bool {
	info, err := os.Lstat(CredentialsPath(privateDir))
	return err == nil && info.Mode().IsRegular() && info.Size() > 0
}

// Reports whether the server stored its credentials encrypted
func PersistenceEncrypted(dataDir string) bool {
	info, err := os.Lstat(filepath.Join(dataDir, AuthEncFile))
	return err == nil && info.Mode().IsRegular()
}

// Reports whether a data dir relative path is a server credential file
func IsSecretFile(rel string) bool {
	rel = filepath.Clean(rel)
	for _, name := range SecretFiles {
		if rel == name {
			return true
		}
	}
	return false
}

// Reports whether the server files are in place
func Installed(dataDir string) bool {
	for _, name := range []string{ServerJar, Assets} {
		if info, err := os.Lstat(filepath.Join(dataDir, name)); err != nil || !info.Mode().IsRegular() {
			return false
		}
	}
	return true
}

// World directories for backups and disk accounting
func WorldDirs(dataDir string) ([]string, error) {
	dir := filepath.Join(dataDir, UniverseDir)
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() {
		return nil, fmt.Errorf("no %s directory found in %s", UniverseDir, dataDir)
	}
	return []string{dir}, nil
}

// World directories for either game
func ServerWorldDirs(dataDir string, loader v1.ModLoader) ([]string, error) {
	if minecraft.IsHytale(loader) {
		return WorldDirs(dataDir)
	}
	return files.FindWorldDirs(dataDir)
}

// Data dir relative paths a default backup archives
func BackupPaths(dataDir string) []string {
	var out []string
	// Both mod folders, so a restore brings back which mods were disabled
	for _, name := range []string{UniverseDir, ModsDir, DisabledModsDir, ConfigFile, WhitelistFile, BansFile, PermissionsFile} {
		if _, err := os.Lstat(filepath.Join(dataDir, name)); err == nil {
			out = append(out, name)
		}
	}
	return out
}

// Returned for symlinks and other non-regular files where a plain file belongs
var ErrNotRegular = errors.New("not a regular file, symlinks are refused")

// Writes via a temp file and rename, keeping an existing file's mode and owner.
// The target dir must be a real directory and a symlink at the target is
// refused, so container-made links never redirect a panel write.
func WriteFileAtomic(path string, data []byte, defaultMode os.FileMode) error {
	dir := filepath.Dir(path)
	if info, err := os.Lstat(dir); err != nil {
		return err
	} else if !info.IsDir() {
		return fmt.Errorf("%s: %w", dir, ErrNotRegular)
	}
	mode := defaultMode
	uid, gid, owned := -1, -1, false
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("%s: %w", filepath.Base(path), ErrNotRegular)
		}
		mode = info.Mode().Perm()
		uid, gid, owned = files.Owner(path)
	} else if !os.IsNotExist(err) {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	cleanup := func() { os.Remove(tmp.Name()) }
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Chmod(tmp.Name(), mode); err != nil {
		cleanup()
		return err
	}
	if owned {
		// Best effort, a non-root panel cannot hand files to other users
		_ = os.Lchown(tmp.Name(), uid, gid)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		cleanup()
		return err
	}
	return nil
}
