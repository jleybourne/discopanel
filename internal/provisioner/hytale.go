package provisioner

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/discohaus/discopanel/internal/docker"
	"github.com/discohaus/discopanel/internal/hytaletracker"
	"github.com/discohaus/discopanel/internal/metrics"
	"github.com/discohaus/discopanel/pkg/files"
	"github.com/discohaus/discopanel/pkg/hytale"
	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
	"github.com/discohaus/discopanel/pkg/runtimespec"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	// The downloader waits up to the device code expiry for a sign-in
	hytaleDownloadTimeout = 45 * time.Minute
	// Version checks never wait for a sign-in
	hytaleVersionTimeout = 90 * time.Second

	hytaleDownloaderDir = "downloader"
	hytaleDownloaderZip = "hytale-downloader.zip"
	hytaleServerZip     = "server.zip"
	hytaleStagingDir    = "staging"

	// Where the private dir appears inside the downloader container
	hytaleMount = "/hytale"
	// The panel refetches the downloader this often, its self update is off
	hytaleDownloaderMaxAge = 7 * 24 * time.Hour
)

// Names in the data dir the server owns at runtime, never replaced by an install
var hytaleProtected = map[string]bool{
	hytale.UniverseDir: true, hytale.ModsDir: true, hytale.DisabledModsDir: true,
	hytale.ConfigFile: true, hytale.WhitelistFile: true, hytale.BansFile: true, hytale.PermissionsFile: true,
	hytale.AuthEncFile: true, hytale.AuthKeyFile: true, "logs": true, runtimespec.StateDir: true,
}

// Wires the Hytale tracker that shows downloader sign-ins
func (p *Provisioner) SetHytaleTracker(t *hytaletracker.Tracker) {
	p.hytale = t
}

// Panel-private Hytale dir for a server, outside its data dir
func (p *Provisioner) hytalePrivateDir(server *v1.Server) (string, error) {
	if p.cfg == nil || p.cfg.Storage.DataDir == "" {
		return "", errors.New("the panel data directory is not configured")
	}
	dir, err := hytale.PrivateDir(p.cfg.Storage.DataDir, server.Id)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	return dir, nil
}

// Downloads or updates Hytale server files, then writes the launch spec
func (p *Provisioner) ensureHytale(ctx context.Context, server *v1.Server, cfg *v1.ServerProperties) (*Result, error) {
	dataDir := server.DataPath
	privateDir, err := p.hytalePrivateDir(server)
	if err != nil {
		return nil, err
	}
	settings, err := hytale.ReadSettings(privateDir)
	if err != nil {
		return nil, err
	}
	patchline := settings.Patchline

	force := cfg.ForceProvision != nil && *cfg.ForceProvision
	manifest, err := runtimespec.ReadManifest(dataDir)
	if err != nil {
		p.progress(server, "provision manifest unreadable (%v), re-provisioning", err)
		manifest = nil
	}
	installed := !force && manifest != nil &&
		manifest.Loader == v1.ModLoader_MOD_LOADER_HYTALE &&
		manifest.HytalePatchline == patchline &&
		hytale.Installed(dataDir)

	binary, err := p.ensureHytaleDownloader(ctx, server, privateDir)
	switch {
	case err != nil && ctx.Err() != nil:
		return nil, ctx.Err()
	case err != nil && !installed:
		return nil, err
	case err != nil:
		// Installed files still run, only the update check is lost
		p.progress(server, "Hytale update check skipped: %v", err)
	}

	latest := ""
	if installed && binary != "" {
		latest, err = p.hytaleLatestVersion(ctx, server, cfg, privateDir, binary, patchline)
		switch {
		case err != nil:
			latest = ""
			p.progress(server, "Hytale update check skipped: %v", err)
		case manifest.LoaderVersion == "":
			// An install whose version was never read gets the known build
			p.progress(server, "installed Hytale version unknown, installing %s from the %s channel", latest, patchline)
			installed = false
		case latest != manifest.LoaderVersion:
			p.progress(server, "Hytale %s is available on the %s channel (installed %s), updating", latest, patchline, manifest.LoaderVersion)
			installed = false
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
	}

	version := ""
	if installed {
		version = manifest.LoaderVersion
		p.progress(server, "server files verified (Hytale %s, %s channel, Java %d)", displayVersion(version), patchline, hytale.JavaMajor)
	} else {
		if manifest != nil {
			p.snapshotWorld(server)
		}
		var prev []string
		if manifest != nil && manifest.Loader == v1.ModLoader_MOD_LOADER_HYTALE {
			prev = manifest.PackFiles
		}
		placed, err := p.installHytale(ctx, server, cfg, privateDir, binary, patchline, prev)
		if err != nil {
			return nil, err
		}
		version = latest
		if version == "" {
			if version, err = p.hytaleLatestVersion(ctx, server, cfg, privateDir, binary, patchline); err != nil {
				p.progress(server, "could not read the installed Hytale version, the next start checks again: %v", err)
				version = ""
			}
		}
		if err := runtimespec.WriteManifest(dataDir, &v1.Manifest{
			Version:         1,
			Loader:          v1.ModLoader_MOD_LOADER_HYTALE,
			LoaderVersion:   version,
			McVersion:       version,
			JavaMajor:       hytale.JavaMajor,
			PackFiles:       placed,
			ProvisionedAt:   timestamppb.Now(),
			HytalePatchline: patchline,
		}); err != nil {
			return nil, fmt.Errorf("failed to write provision manifest: %w", err)
		}
		p.action(ctx, server, "provisioner", v1.ServerActionKind_SERVER_ACTION_KIND_PROVISION_INSTALL,
			metrics.Attrs{"loader": "hytale", "loader_version": version, "patchline": patchline},
			"installed Hytale server files (%s, %s channel, Java %d)", displayVersion(version), patchline, hytale.JavaMajor)
	}

	// Edits saved while the server ran land before it reads config.json
	applied, err := hytale.ApplyPending(dataDir, privateDir)
	if len(applied) > 0 {
		p.progress(server, "applied saved changes to %s", strings.Join(applied, ", "))
	}
	if err != nil {
		p.progress(server, "saved changes not applied yet: %v", err)
	}

	// Older runtimes only know the Minecraft loaders, custom launches verbatim
	spec := &v1.LaunchSpec{
		Version:   1,
		Kind:      v1.LaunchKind_LAUNCH_KIND_CUSTOM,
		Exec:      hytale.LaunchExec(dataDir),
		Loader:    v1.ModLoader_MOD_LOADER_CUSTOM,
		McVersion: version,
		JavaMajor: hytale.JavaMajor,
	}
	if err := runtimespec.WriteLaunchSpec(dataDir, spec); err != nil {
		return nil, fmt.Errorf("failed to write launch spec: %w", err)
	}
	return &Result{
		Loader:        v1.ModLoader_MOD_LOADER_HYTALE,
		LoaderVersion: version,
		McVersion:     version,
		JavaMajor:     hytale.JavaMajor,
	}, nil
}

func displayVersion(v string) string {
	if v == "" {
		return "version unknown"
	}
	return v
}

// Fetches the official downloader into the private dir and refreshes it
// weekly, returns its private dir relative path
func (p *Provisioner) ensureHytaleDownloader(ctx context.Context, server *v1.Server, privateDir string) (string, error) {
	arch, err := p.hostArch(ctx)
	if err != nil {
		return "", err
	}
	dir := filepath.Join(privateDir, hytaleDownloaderDir)
	existing, ferr := findDownloader(dir, arch)
	if ferr == nil {
		if info, err := os.Stat(dir); err == nil && time.Since(info.ModTime()) < hytaleDownloaderMaxAge {
			return existing, nil
		}
	}

	p.progress(server, "fetching the Hytale downloader...")
	zipPath := filepath.Join(privateDir, hytaleDownloaderZip)
	fetch := func() (string, error) {
		if err := p.download(ctx, hytale.DownloaderURL, zipPath, nil, nil, p.reporter(server, hytaleDownloaderZip)); err != nil {
			return "", fmt.Errorf("failed to download the Hytale downloader: %w", err)
		}
		defer os.Remove(zipPath)
		fresh := dir + ".new"
		if err := os.RemoveAll(fresh); err != nil {
			return "", err
		}
		if _, err := files.ExtractArchive(ctx, zipPath, fresh, nil); err != nil {
			os.RemoveAll(fresh)
			return "", fmt.Errorf("failed to unpack the Hytale downloader: %w", err)
		}
		if _, err := findDownloader(fresh, arch); err != nil {
			os.RemoveAll(fresh)
			return "", err
		}
		if err := os.RemoveAll(dir); err != nil {
			return "", err
		}
		if err := os.Rename(fresh, dir); err != nil {
			return "", err
		}
		return findDownloader(dir, arch)
	}
	rel, err := fetch()
	if err != nil && ferr == nil {
		// A stale downloader still works, try again next start
		p.progress(server, "kept the existing Hytale downloader: %v", err)
		return existing, nil
	}
	return rel, err
}

// Locates and marks executable the downloader build for arch
func findDownloader(dir, arch string) (string, error) {
	var names []string
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			if rel, rerr := filepath.Rel(dir, path); rerr == nil {
				names = append(names, filepath.ToSlash(rel))
			}
		}
		return nil
	})
	if len(names) == 0 {
		return "", errors.New("downloader not fetched yet")
	}
	slices.Sort(names)
	name, err := hytale.DownloaderBinary(names, arch)
	if err != nil {
		return "", err
	}
	full := filepath.Join(dir, filepath.FromSlash(name))
	if err := os.Chmod(full, 0755); err != nil {
		return "", err
	}
	return filepath.Join(hytaleDownloaderDir, filepath.FromSlash(name)), nil
}

func (p *Provisioner) hostArch(ctx context.Context) (string, error) {
	if p.docker == nil {
		return runtime.GOARCH, nil
	}
	arch, err := p.docker.HostArch(ctx)
	if err != nil || arch == "" {
		// Local docker shares the panel's architecture
		return runtime.GOARCH, nil
	}
	return arch, nil
}

// Runs the downloader in the server's Java 25 runtime image. Only the
// private dir is mounted: the downloader never sees the server data dir and
// the game container never sees the downloader's credentials.
func (p *Provisioner) runHytaleDownloader(ctx context.Context, server *v1.Server, cfg *v1.ServerProperties, privateDir string, args []string, onLine func(string)) error {
	image, err := p.docker.DesiredImage(server)
	if err != nil {
		return err
	}
	uid, gid := 1000, 1000
	if cfg.Uid != nil {
		uid = int(*cfg.Uid)
	}
	if cfg.Gid != nil {
		gid = int(*cfg.Gid)
	}
	if os.Getuid() == 0 && uid > 0 {
		chownTree(privateDir, uid, gid)
	}
	return p.docker.RunOneShot(ctx, docker.OneShotOptions{
		Image:      image,
		Cmd:        args,
		Mounts:     []docker.OneShotMount{{Source: privateDir, Target: hytaleMount}},
		WorkingDir: hytaleMount,
		User:       fmt.Sprintf("%d:%d", uid, gid),
		// Anything the tool caches stays in the private dir
		Env:     []string{"HOME=" + hytaleMount},
		Name:    fmt.Sprintf("discopanel-hytale-dl-%s", server.Id),
		SplitCR: true,
		Labels: map[string]string{
			"discopanel.server.id": server.Id,
			"discopanel.managed":   "true",
			"discopanel.oneshot":   "true",
		},
	}, onLine)
}

func downloaderArgs(privateDir, binary, patchline, downloadPath string) ([]string, error) {
	bin, err := hytale.MountedPath(privateDir, hytaleMount, filepath.Join(privateDir, binary))
	if err != nil {
		return nil, err
	}
	creds, err := hytale.MountedPath(privateDir, hytaleMount, hytale.CredentialsPath(privateDir))
	if err != nil {
		return nil, err
	}
	out := ""
	if downloadPath != "" {
		if out, err = hytale.MountedPath(privateDir, hytaleMount, downloadPath); err != nil {
			return nil, err
		}
	}
	return hytale.DownloaderArgs(hytale.DownloaderOpts{Binary: bin, CredentialsPath: creds, Patchline: patchline, DownloadPath: out}), nil
}

// Asks the downloader for the newest build, never waiting on a sign-in
func (p *Provisioner) hytaleLatestVersion(ctx context.Context, server *v1.Server, cfg *v1.ServerProperties, privateDir, binary, patchline string) (string, error) {
	args, err := downloaderArgs(privateDir, binary, patchline, "")
	if err != nil {
		return "", err
	}
	runCtx, cancel := context.WithTimeout(ctx, hytaleVersionTimeout)
	defer cancel()
	var out strings.Builder
	var needsSignIn atomic.Bool
	err = p.runHytaleDownloader(runCtx, server, cfg, privateDir, args, func(line string) {
		if hytale.WantsSignIn(line) {
			needsSignIn.Store(true)
			cancel()
			return
		}
		out.WriteString(line)
		out.WriteByte('\n')
	})
	if needsSignIn.Load() {
		return "", errors.New("the downloader sign-in expired, choose Reinstall on next start under Settings > Updates to sign in again")
	}
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", err
	}
	v := hytale.ParseVersion(out.String())
	if v == "" {
		return "", errors.New("the downloader printed no version")
	}
	return v, nil
}

// Downloads the server zip and places its files, returns the placed names
func (p *Provisioner) installHytale(ctx context.Context, server *v1.Server, cfg *v1.ServerProperties, privateDir, binary, patchline string, prev []string) ([]string, error) {
	zipPath := filepath.Join(privateDir, hytaleServerZip)
	_ = os.Remove(zipPath)
	defer os.Remove(zipPath)
	args, err := downloaderArgs(privateDir, binary, patchline, zipPath)
	if err != nil {
		return nil, err
	}

	p.progress(server, "provisioning Hytale server (%s channel)...", patchline)
	if !hytale.DownloaderSignedIn(privateDir) {
		p.progress(server, "the downloader will ask you to sign in with your Hytale account")
	}
	runCtx, cancel := context.WithTimeout(ctx, hytaleDownloadTimeout)
	defer cancel()
	var flow hytale.DeviceFlow
	shown := false
	started := time.Now()
	// A line delivered after the run ended must not bring the card back
	var (
		signInMu sync.Mutex
		finished bool
	)
	err = p.runHytaleDownloader(runCtx, server, cfg, privateDir, args, func(line string) {
		signInMu.Lock()
		defer signInMu.Unlock()
		if finished {
			return
		}
		p.progress(server, "[downloader] %s", line)
		if flow.Feed(line) && flow.Ready() && p.hytale != nil {
			if !shown {
				started = time.Now()
				p.progress(server, "sign-in link detected, open it from the server page to continue")
			}
			shown = true
			p.hytale.SetDownloaderSignIn(server.Id, flow, started)
		}
	})
	signInMu.Lock()
	finished = true
	if p.hytale != nil {
		p.hytale.ClearDownloaderSignIn(server.Id)
	}
	signInMu.Unlock()
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("the Hytale downloader failed: %w", err)
	}
	if info, err := os.Stat(zipPath); err != nil || info.Size() == 0 {
		return nil, errors.New("the Hytale downloader finished without writing the server files")
	}

	p.progress(server, "unpacking Hytale server files...")
	staging := filepath.Join(privateDir, hytaleStagingDir)
	if err := os.RemoveAll(staging); err != nil {
		return nil, err
	}
	defer os.RemoveAll(staging)
	if _, err := files.ExtractArchive(ctx, zipPath, staging, nil); err != nil {
		return nil, fmt.Errorf("failed to unpack the Hytale server files: %w", err)
	}
	return placeHytaleFiles(staging, server.DataPath, prev)
}

// Finds the folder holding Server/HytaleServer.jar, root or one level down
func hytaleRoot(staging string) (string, error) {
	if fileExists(filepath.Join(staging, "Server", hytale.ServerJar)) {
		return staging, nil
	}
	entries, err := os.ReadDir(staging)
	if err != nil {
		return "", err
	}
	for _, e := range entries {
		if e.IsDir() && fileExists(filepath.Join(staging, e.Name(), "Server", hytale.ServerJar)) {
			return filepath.Join(staging, e.Name()), nil
		}
	}
	return "", fmt.Errorf("the downloaded archive has no Server/%s", hytale.ServerJar)
}

// Moves Server/* and Assets.zip into the data dir root, so the server runs
// with the data dir as its working dir exactly as it would from Server/
func placeHytaleFiles(staging, dataDir string, prev []string) ([]string, error) {
	root, err := hytaleRoot(staging)
	if err != nil {
		return nil, err
	}
	if !fileExists(filepath.Join(root, hytale.Assets)) {
		return nil, fmt.Errorf("the downloaded archive has no %s", hytale.Assets)
	}
	serverDir := filepath.Join(root, "Server")
	entries, err := os.ReadDir(serverDir)
	if err != nil {
		return nil, err
	}
	type move struct{ src, name string }
	moves := []move{{filepath.Join(root, hytale.Assets), hytale.Assets}}
	for _, e := range entries {
		moves = append(moves, move{filepath.Join(serverDir, e.Name()), e.Name()})
	}

	var placed []string
	for _, m := range moves {
		dest := filepath.Join(dataDir, m.name)
		if hytaleProtected[m.name] {
			// Server owned state survives an update, a shipped default only seeds it
			if _, err := os.Lstat(dest); err == nil {
				continue
			}
		}
		if err := os.RemoveAll(dest); err != nil {
			return placed, fmt.Errorf("failed to replace %s: %w", m.name, err)
		}
		if err := moveAcross(m.src, dest); err != nil {
			return placed, fmt.Errorf("failed to place %s: %w", m.name, err)
		}
		if !hytaleProtected[m.name] {
			placed = append(placed, m.name)
		}
	}

	// Files an older build shipped and this one dropped
	for _, name := range prev {
		if hytaleProtected[name] || slices.Contains(placed, name) || name != filepath.Base(name) || strings.HasPrefix(name, ".") {
			continue
		}
		_ = os.RemoveAll(filepath.Join(dataDir, name))
	}
	slices.Sort(placed)
	return placed, nil
}

// Renames, falling back to copy and remove when the private dir and the
// data dir sit on different filesystems
func moveAcross(src, dst string) error {
	err := os.Rename(src, dst)
	if err == nil || !errors.Is(err, syscall.EXDEV) {
		return err
	}
	info, err := os.Lstat(src)
	if err != nil {
		return err
	}
	if info.IsDir() {
		err = files.CopyDir(src, dst)
	} else {
		err = files.CopyFile(src, dst)
	}
	if err != nil {
		os.RemoveAll(dst)
		return err
	}
	return os.RemoveAll(src)
}
