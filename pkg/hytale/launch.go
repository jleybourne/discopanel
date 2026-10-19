package hytale

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
)

// Official downloader, fetched at provision time and never bundled
var DownloaderURL = "https://downloader.hytale.com/hytale-downloader.zip"

// Server arguments after the JVM flags, the data dir is the working dir
func LaunchExec(dataDir string) string {
	var tokens []string
	// The AOT cache only helps when it matches the jar it shipped with
	if info, err := os.Stat(filepath.Join(dataDir, AOTCache)); err == nil && info.Mode().IsRegular() {
		tokens = append(tokens, "-XX:AOTCache="+AOTCache)
	}
	tokens = append(tokens,
		"-jar", ServerJar,
		"--assets", Assets,
		"--bind", "0.0.0.0:"+strconv.Itoa(ContainerPort),
		"--auth-mode", "authenticated",
	)
	return strings.Join(tokens, " ")
}

// Maps docker and Go architecture names onto downloader build names
func normalizeArch(arch string) string {
	switch strings.ToLower(arch) {
	case "amd64", "x86_64", "x64", "x86-64":
		return "amd64"
	case "arm64", "aarch64", "armv8":
		return "arm64"
	}
	return strings.ToLower(arch)
}

func archAliases(arch string) []string {
	switch normalizeArch(arch) {
	case "amd64":
		return []string{"amd64", "x86_64", "x64"}
	case "arm64":
		return []string{"arm64", "aarch64"}
	}
	return []string{normalizeArch(arch)}
}

// Picks the Linux downloader build for arch among zip entry names
func DownloaderBinary(names []string, arch string) (string, error) {
	aliases := archAliases(arch)
	var linux []string
	for _, n := range names {
		base := strings.ToLower(path.Base(filepath.ToSlash(n)))
		if !strings.Contains(base, "linux") || strings.HasSuffix(base, ".exe") || strings.HasSuffix(base, ".md") || strings.HasSuffix(base, ".txt") {
			continue
		}
		linux = append(linux, n)
		for _, a := range aliases {
			if strings.Contains(base, a) {
				return n, nil
			}
		}
	}
	// One unlabelled Linux build is assumed to be x86_64 only
	if len(linux) == 1 && normalizeArch(arch) == "amd64" {
		return linux[0], nil
	}
	return "", fmt.Errorf("the Hytale downloader has no Linux build for %s (found %d Linux files)", normalizeArch(arch), len(linux))
}

// Downloader invocation options
type DownloaderOpts struct {
	Binary          string // Container path of the downloader
	CredentialsPath string // Container path of the credentials file
	Patchline       string
	DownloadPath    string // Container path of the output zip, empty prints the version
}

// Builds the downloader command line. The panel refreshes the downloader
// itself, so its self update stays off: a binary rewriting itself inside a
// one-shot container could hang on a prompt or leave a half written file.
func DownloaderArgs(o DownloaderOpts) []string {
	args := []string{o.Binary, "-skip-update-check", "-credentials-path", o.CredentialsPath}
	if p, _ := NormalizePatchline(o.Patchline); p == PatchlinePreRelease {
		args = append(args, "-patchline", PatchlinePreRelease)
	}
	if o.DownloadPath == "" {
		return append(args, "-print-version")
	}
	return append(args, "-download-path", o.DownloadPath)
}
