package hytale

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
)

var (
	ErrInvalidModName = errors.New("mod file name must be a plain .jar or .zip file name")
	ErrModExists      = errors.New("a mod with this file name already exists")
	ErrModNotFound    = errors.New("mod not found")
	ErrUnsafeModPath  = errors.New("mod folders and files must be real folders and files, symlinks are refused")
)

// Plugin for .jar, asset pack for .zip, unspecified for anything else
func ModKind(name string) v1.HytaleModKind {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".jar":
		return v1.HytaleModKind_HYTALE_MOD_KIND_PLUGIN
	case ".zip":
		return v1.HytaleModKind_HYTALE_MOD_KIND_ASSET_PACK
	}
	return v1.HytaleModKind_HYTALE_MOD_KIND_UNSPECIFIED
}

// Accepts plain visible .jar or .zip names only, no path segments
func ValidModName(name string) bool {
	if name == "" || name != filepath.Base(name) || strings.HasPrefix(name, ".") || len(name) > 255 {
		return false
	}
	if strings.ContainsAny(name, "/\\:\x00\n\r") {
		return false
	}
	return ModKind(name) != v1.HytaleModKind_HYTALE_MOD_KIND_UNSPECIFIED
}

// Mod folder for one state, refused when it is not a real directory. A
// container-made symlink here would point moves and deletes at host paths.
func modDir(dataDir string, enabled bool, create bool) (string, error) {
	name := DisabledModsDir
	if enabled {
		name = ModsDir
	}
	dir := filepath.Join(dataDir, name)
	info, err := os.Lstat(dir)
	switch {
	case err == nil && info.IsDir():
		return dir, nil
	case err == nil:
		return "", fmt.Errorf("%s: %w", name, ErrUnsafeModPath)
	case !os.IsNotExist(err):
		return "", err
	case !create:
		return dir, os.ErrNotExist
	}
	if err := os.Mkdir(dir, 0755); err != nil && !os.IsExist(err) {
		return "", err
	}
	// Recheck, the folder may have been swapped for a link meanwhile
	if info, err := os.Lstat(dir); err != nil || !info.IsDir() {
		return "", fmt.Errorf("%s: %w", name, ErrUnsafeModPath)
	}
	return dir, nil
}

// Lists plugins and asset packs, enabled and disabled, sorted by name
func ListMods(dataDir string) ([]*v1.HytaleMod, error) {
	var mods []*v1.HytaleMod
	for _, enabled := range []bool{true, false} {
		dir, err := modDir(dataDir, enabled, false)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			if !e.Type().IsRegular() || !ValidModName(e.Name()) {
				continue
			}
			info, err := e.Info()
			if err != nil {
				continue
			}
			mods = append(mods, &v1.HytaleMod{
				FileName: e.Name(),
				Kind:     ModKind(e.Name()),
				Size:     info.Size(),
				Enabled:  enabled,
			})
		}
	}
	slices.SortFunc(mods, func(a, b *v1.HytaleMod) int {
		if c := strings.Compare(strings.ToLower(a.FileName), strings.ToLower(b.FileName)); c != 0 {
			return c
		}
		// Twins sort enabled first
		if a.Enabled != b.Enabled {
			if a.Enabled {
				return -1
			}
			return 1
		}
		return 0
	})
	return mods, nil
}

// Exact mod file in one folder, regular files only
func modFile(dataDir, name string, enabled bool) (string, error) {
	if !ValidModName(name) {
		return "", ErrInvalidModName
	}
	dir, err := modDir(dataDir, enabled, false)
	if errors.Is(err, os.ErrNotExist) {
		return "", ErrModNotFound
	}
	if err != nil {
		return "", err
	}
	p := filepath.Join(dir, name)
	info, err := os.Lstat(p)
	if os.IsNotExist(err) {
		return "", ErrModNotFound
	}
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("%s: %w", name, ErrUnsafeModPath)
	}
	return p, nil
}

// Reports whether a name is taken in either folder, links included
func modNameTaken(dataDir, name string) bool {
	for _, d := range []string{ModsDir, DisabledModsDir} {
		if _, err := os.Lstat(filepath.Join(dataDir, d, name)); err == nil {
			return true
		}
	}
	return false
}

func modEntry(path string, enabled bool) (*v1.HytaleMod, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	name := filepath.Base(path)
	return &v1.HytaleMod{FileName: name, Kind: ModKind(name), Size: info.Size(), Enabled: enabled}, nil
}

// Moves a mod between mods/ and mods_disabled/, never over another file
func SetModEnabled(dataDir, name string, enabled bool) (*v1.HytaleMod, error) {
	if !ValidModName(name) {
		return nil, ErrInvalidModName
	}
	// Twins in both folders resolve to the copy that has to move
	src, err := modFile(dataDir, name, !enabled)
	if errors.Is(err, ErrModNotFound) {
		existing, eerr := modFile(dataDir, name, enabled)
		if eerr != nil {
			return nil, eerr
		}
		return modEntry(existing, enabled)
	}
	if err != nil {
		return nil, err
	}
	destDir, err := modDir(dataDir, enabled, true)
	if err != nil {
		return nil, err
	}
	dest := filepath.Join(destDir, name)
	if _, err := os.Lstat(dest); err == nil {
		return nil, fmt.Errorf("%w in %s", ErrModExists, filepath.Base(destDir))
	}
	if err := os.Rename(src, dest); err != nil {
		return nil, err
	}
	return modEntry(dest, enabled)
}

// Deletes exactly the copy in the folder enabled names
func DeleteMod(dataDir, name string, enabled bool) error {
	path, err := modFile(dataDir, name, enabled)
	if err != nil {
		return err
	}
	return os.Remove(path)
}

// Copies src into mods/ as name, refusing names already present in either folder
func PlaceMod(dataDir, src, name string) (*v1.HytaleMod, error) {
	if !ValidModName(name) {
		return nil, ErrInvalidModName
	}
	if modNameTaken(dataDir, name) {
		return nil, ErrModExists
	}
	modsDir, err := modDir(dataDir, true, true)
	if err != nil {
		return nil, err
	}
	in, err := os.Open(src)
	if err != nil {
		return nil, err
	}
	defer in.Close()
	tmp, err := os.CreateTemp(modsDir, ".upload-*.tmp")
	if err != nil {
		return nil, err
	}
	if _, err := io.Copy(tmp, in); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return nil, err
	}
	if err := tmp.Chmod(0644); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return nil, err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return nil, err
	}
	dest := filepath.Join(modsDir, name)
	// Link fails when a racing upload or a link already holds the name
	if err := os.Link(tmp.Name(), dest); err != nil {
		if os.IsExist(err) {
			os.Remove(tmp.Name())
			return nil, ErrModExists
		}
		// Filesystems without hard links fall back to a checked rename
		if _, serr := os.Lstat(dest); serr == nil {
			os.Remove(tmp.Name())
			return nil, ErrModExists
		}
		if err := os.Rename(tmp.Name(), dest); err != nil {
			os.Remove(tmp.Name())
			return nil, err
		}
	} else {
		os.Remove(tmp.Name())
	}
	return modEntry(dest, true)
}
