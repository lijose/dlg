package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

const releaseAPI = "https://api.github.com/repos/lijose/dlg/releases/latest"

var releaseClient = &http.Client{Timeout: 6 * time.Second}
var updateMu sync.Mutex
var stableVersion = regexp.MustCompile(`^v?(\d+)\.(\d+)\.(\d+)$`)

type updateInfo struct {
	Current     string    `json:"current"`
	Latest      string    `json:"latest,omitempty"`
	Available   bool      `json:"available"`
	Development bool      `json:"development"`
	CheckedAt   time.Time `json:"checked_at"`
	Error       string    `json:"error,omitempty"`
	Executable  string    `json:"executable"`
	Commands    string    `json:"commands,omitempty"`
	ReleaseURL  string    `json:"release_url,omitempty"`
	Managed     bool      `json:"managed"`
}

func newerVersion(latest, current string) bool {
	a, b := stableVersion.FindStringSubmatch(latest), stableVersion.FindStringSubmatch(current)
	if a == nil || b == nil {
		return false
	}
	for i := 1; i <= 3; i++ {
		x, err := strconv.ParseUint(a[i], 10, 64)
		if err != nil {
			return false
		}
		y, err := strconv.ParseUint(b[i], 10, 64)
		if err != nil {
			return false
		}
		if x != y {
			return x > y
		}
	}
	return false
}
func shellQuote(s string) string      { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
func powershellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }
func updateCachePath() string {
	p, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	return filepath.Join(p, "dlg", "update-check.json")
}

func checkUpdates(ctx context.Context, force bool) updateInfo {
	updateMu.Lock()
	defer updateMu.Unlock()
	executable, _ := os.Executable()
	if p, err := filepath.EvalSymlinks(executable); err == nil {
		executable = p
	}
	info := updateInfo{Current: versionString(), Executable: executable, Development: !stableVersion.MatchString(versionString())}
	for _, part := range []string{"/Cellar/", "/snap/", "/nix/store/", "/scoop/"} {
		if strings.Contains(filepath.ToSlash(executable), part) {
			info.Managed = true
		}
	}
	path := updateCachePath()
	if !force && path != "" {
		if data, err := os.ReadFile(path); err == nil {
			var old updateInfo
			if json.Unmarshal(data, &old) == nil && time.Since(old.CheckedAt) >= 0 && time.Since(old.CheckedAt) < 24*time.Hour {
				old.Current = info.Current
				old.Executable = executable
				old.Development = info.Development
				old.Managed = info.Managed
				old.Available = old.Error == "" && newerVersion(old.Latest, old.Current)
				old.ReleaseURL = ""
				if stableVersion.MatchString(old.Latest) {
					old.ReleaseURL = "https://github.com/lijose/dlg/releases/tag/" + old.Latest
				}
				old.Commands = ""
				if old.Available && !old.Managed {
					old.Commands = updateCommands(old.Latest, executable)
				}
				return old
			}
		}
	}
	info.CheckedAt = time.Now().UTC()
	req, err := http.NewRequestWithContext(ctx, "GET", releaseAPI, nil)
	if err == nil {
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("User-Agent", "dlg-update-check")
		var res *http.Response
		res, err = releaseClient.Do(req)
		if err == nil {
			defer res.Body.Close()
			if res.StatusCode != 200 {
				err = fmt.Errorf("release service returned HTTP %d", res.StatusCode)
			} else {
				var release struct {
					Tag        string `json:"tag_name"`
					Draft      bool   `json:"draft"`
					Prerelease bool   `json:"prerelease"`
					Assets     []struct {
						Name string `json:"name"`
					} `json:"assets"`
				}
				err = json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&release)
				if err == nil {
					if release.Draft || release.Prerelease || !stableVersion.MatchString(release.Tag) {
						err = fmt.Errorf("no stable release version found")
					} else {
						asset := releaseAsset()
						haveBinary, haveSums := false, false
						for _, a := range release.Assets {
							haveBinary = haveBinary || a.Name == asset
							haveSums = haveSums || a.Name == "SHA256SUMS"
						}
						info.Latest = release.Tag
						info.ReleaseURL = "https://github.com/lijose/dlg/releases/tag/" + release.Tag
						if !haveBinary || !haveSums {
							err = fmt.Errorf("release has no verified-download instructions for this platform")
						} else {
							info.Available = newerVersion(info.Latest, info.Current)
							if info.Available && !info.Managed {
								info.Commands = updateCommands(info.Latest, executable)
							}
						}
					}
				}
			}
		}
	}
	if err != nil {
		info.Error = "Couldn’t check for updates. " + err.Error()
	}
	if path != "" {
		_ = os.MkdirAll(filepath.Dir(path), 0700)
		if data, err := json.Marshal(info); err == nil {
			_ = os.WriteFile(path, data, 0600)
		}
	}
	return info
}
func releaseAsset() string {
	suffix := ""
	if runtime.GOOS == "windows" {
		suffix = ".exe"
	}
	return "dlg-" + runtime.GOOS + "-" + runtime.GOARCH + suffix
}
func updateCommands(tag, destination string) string {
	if !stableVersion.MatchString(tag) || destination == "" {
		return ""
	}
	asset := releaseAsset()
	base := "https://github.com/lijose/dlg/releases/download/" + tag + "/"
	cache, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	dir := filepath.Join(cache, "dlg", "updates")
	if runtime.GOOS == "windows" {
		return "$dlgUpdateDir = Join-Path " + powershellQuote(dir) + " ([guid]::NewGuid().ToString())\nNew-Item -ItemType Directory -Force $dlgUpdateDir | Out-Null\nInvoke-WebRequest " + powershellQuote(base+asset) + " -OutFile (Join-Path $dlgUpdateDir " + powershellQuote(asset) + ")\nInvoke-WebRequest " + powershellQuote(base+"SHA256SUMS") + " -OutFile (Join-Path $dlgUpdateDir 'SHA256SUMS')\n$expected = @(Get-Content (Join-Path $dlgUpdateDir 'SHA256SUMS') | Where-Object { $_ -match '^[a-fA-F0-9]{64}  " + regexp.QuoteMeta(asset) + "$' })\nif ($expected.Count -ne 1) { throw 'Missing checksum' }\nif ((Get-FileHash (Join-Path $dlgUpdateDir " + powershellQuote(asset) + ") -Algorithm SHA256).Hash -ne $expected[0].Substring(0,64)) { throw 'Checksum mismatch' }\n# Stop all dlg serve instances before replacing the executable.\nCopy-Item (Join-Path $dlgUpdateDir " + powershellQuote(asset) + ") " + powershellQuote(destination) + " -Force"
	}
	verifier := "sha256sum -c -"
	if runtime.GOOS == "darwin" {
		verifier = "shasum -a 256 -c -"
	}
	return "(\nset -eu\numask 077\nmkdir -p " + shellQuote(dir) + "\nDLG_UPDATE_DIR=$(mktemp -d " + shellQuote(filepath.Join(dir, "download.XXXXXXXX")) + ")\ncd \"$DLG_UPDATE_DIR\"\ncurl --fail --location --proto '=https' --proto-redir '=https' --max-time 120 --output " + shellQuote(asset) + " " + shellQuote(base+asset) + "\ncurl --fail --location --proto '=https' --proto-redir '=https' --max-time 30 --output SHA256SUMS " + shellQuote(base+"SHA256SUMS") + "\nawk '$2 == \"" + asset + "\" { print }' SHA256SUMS | " + verifier + "\n# Stop active dlg runs before installing.\nif [ -w " + shellQuote(filepath.Dir(destination)) + " ]; then\n  install -m 755 " + shellQuote(asset) + " " + shellQuote(destination) + "\nelse\n  sudo install -m 755 " + shellQuote(asset) + " " + shellQuote(destination) + "\nfi\n)"
}
func (s *server) updateRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/about", api(func(r *http.Request) (any, error) {
		path, _ := os.Executable()
		return D{"current": versionString(), "development": !stableVersion.MatchString(versionString()), "executable": path}, nil
	}))
	mux.HandleFunc("GET /api/updates", api(func(r *http.Request) (any, error) {
		return checkUpdates(r.Context(), r.URL.Query().Get("refresh") == "1"), nil
	}))
}
