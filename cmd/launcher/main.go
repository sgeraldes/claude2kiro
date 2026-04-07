// claude2kiro launcher - lightweight binary that lives in PATH and delegates
// to the versioned binary in ~/.claude2kiro/bin/. Handles auto-update checks
// without interfering with running instances.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

const (
	repo          = "sgeraldes/claude2kiro"
	checkInterval = 1 * time.Hour // Don't check more than once per hour
	keepVersions  = 2             // Keep last N versions, delete older
)

func main() {
	binDir := getBinDir()
	os.MkdirAll(binDir, 0755)

	// Handle "update" command directly in the launcher
	if len(os.Args) > 1 && os.Args[1] == "update" {
		forceUpdate(binDir)
		return
	}

	// Find current version to run
	currentBin := getCurrentBinary(binDir)

	if currentBin == "" {
		// No version installed yet - download latest
		fmt.Fprintln(os.Stderr, "No claude2kiro version found. Downloading latest...")
		if err := downloadLatest(binDir); err != nil {
			fmt.Fprintf(os.Stderr, "Failed to download: %v\n", err)
			os.Exit(1)
		}
		currentBin = getCurrentBinary(binDir)
		if currentBin == "" {
			fmt.Fprintln(os.Stderr, "Failed to find downloaded binary")
			os.Exit(1)
		}
	}

	// Background update check (non-blocking)
	go backgroundUpdateCheck(binDir)

	// Validate the binary path is inside our managed bin directory
	// to prevent path traversal from a tampered current.txt
	absCurrentBin, _ := filepath.Abs(currentBin)
	absBinDir, _ := filepath.Abs(binDir)
	if !strings.HasPrefix(absCurrentBin, absBinDir+string(filepath.Separator)) {
		fmt.Fprintf(os.Stderr, "Security error: binary path %q is outside managed directory\n", currentBin)
		os.Exit(1)
	}

	// Exec the real binary with all args (path validated above)
	argv := append([]string{absCurrentBin}, os.Args[1:]...)
	attr := &os.ProcAttr{
		Files: []*os.File{os.Stdin, os.Stdout, os.Stderr},
	}
	proc, err := os.StartProcess(absCurrentBin, argv, attr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to start: %v\n", err)
		os.Exit(1)
	}
	state, err := proc.Wait()
	if err != nil {
		os.Exit(1)
	}
	os.Exit(state.ExitCode())
}

// getBinDir returns ~/.claude2kiro/bin/
func getBinDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Cannot determine home directory: %v\n", err)
		os.Exit(1)
	}
	return filepath.Join(home, ".claude2kiro", "bin")
}

// getCurrentBinary reads current.txt and returns the path to the active binary.
// Falls back to the newest versioned binary if current.txt is missing/stale.
func getCurrentBinary(binDir string) string {
	ext := ""
	if runtime.GOOS == "windows" {
		ext = ".exe"
	}

	// Try current.txt
	currentFile := filepath.Join(binDir, "current.txt")
	if data, err := os.ReadFile(currentFile); err == nil {
		version := strings.TrimSpace(string(data))
		bin := filepath.Join(binDir, fmt.Sprintf("claude2kiro-%s%s", version, ext))
		if _, err := os.Stat(bin); err == nil {
			return bin
		}
	}

	// Fallback: find newest versioned binary
	entries, err := os.ReadDir(binDir)
	if err != nil {
		return ""
	}

	var versions []string
	prefix := "claude2kiro-"
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, prefix) && !e.IsDir() {
			ver := strings.TrimPrefix(name, prefix)
			ver = strings.TrimSuffix(ver, ext)
			if ver != "" {
				versions = append(versions, ver)
			}
		}
	}

	if len(versions) == 0 {
		return ""
	}

	sort.Strings(versions)
	latest := versions[len(versions)-1]

	// Write current.txt for next time
	os.WriteFile(currentFile, []byte(latest), 0644)

	return filepath.Join(binDir, fmt.Sprintf("claude2kiro-%s%s", latest, ext))
}

// backgroundUpdateCheck checks for updates and downloads if available.
// Runs in a goroutine, never blocks the main process.
func backgroundUpdateCheck(binDir string) {
	// Rate limit: check at most once per hour
	stampFile := filepath.Join(binDir, ".last-update-check")
	if info, err := os.Stat(stampFile); err == nil {
		if time.Since(info.ModTime()) < checkInterval {
			return // Checked recently
		}
	}

	// Touch stamp file
	os.WriteFile(stampFile, []byte(time.Now().Format(time.RFC3339)), 0644)

	latestVersion, downloadURL := getLatestRelease()
	if latestVersion == "" || downloadURL == "" {
		return
	}

	// Check if we already have this version
	ext := ""
	if runtime.GOOS == "windows" {
		ext = ".exe"
	}
	targetPath := filepath.Join(binDir, fmt.Sprintf("claude2kiro-%s%s", latestVersion, ext))
	if _, err := os.Stat(targetPath); err == nil {
		return // Already have latest
	}

	// Download silently
	if err := downloadBinary(downloadURL, targetPath); err != nil {
		return // Silent failure
	}

	// Update current.txt to point to new version
	currentFile := filepath.Join(binDir, "current.txt")
	os.WriteFile(currentFile, []byte(latestVersion), 0644)

	// Clean up old versions
	cleanOldVersions(binDir, keepVersions)

	// Notify on stderr (visible but non-intrusive)
	fmt.Fprintf(os.Stderr, "claude2kiro updated to v%s (will take effect on next launch)\n", latestVersion)
}

// forceUpdate checks for and downloads the latest version, then reports status.
func forceUpdate(binDir string) {
	fmt.Println("Checking for updates...")

	latestVersion, downloadURL := getLatestRelease()
	if latestVersion == "" {
		fmt.Println("No releases found at https://github.com/" + repo + "/releases")
		return
	}

	ext := ""
	if runtime.GOOS == "windows" {
		ext = ".exe"
	}

	// Check current version
	currentFile := filepath.Join(binDir, "current.txt")
	currentVersion := ""
	if data, err := os.ReadFile(currentFile); err == nil {
		currentVersion = strings.TrimSpace(string(data))
	}

	if currentVersion == latestVersion {
		fmt.Printf("Already up to date (v%s)\n", latestVersion)
		return
	}

	if currentVersion != "" {
		fmt.Printf("Updating: v%s -> v%s\n", currentVersion, latestVersion)
	} else {
		fmt.Printf("Installing: v%s\n", latestVersion)
	}

	targetPath := filepath.Join(binDir, fmt.Sprintf("claude2kiro-%s%s", latestVersion, ext))

	// Download if not already present
	if _, err := os.Stat(targetPath); os.IsNotExist(err) {
		fmt.Printf("Downloading...")
		if err := downloadBinary(downloadURL, targetPath); err != nil {
			fmt.Fprintf(os.Stderr, "\nDownload failed: %v\n", err)
			os.Exit(1)
		}
		fmt.Println(" done")
	}

	// Update current.txt
	os.WriteFile(currentFile, []byte(latestVersion), 0644)

	// Clean old versions
	removed := cleanOldVersions(binDir, keepVersions)
	if removed > 0 {
		fmt.Printf("Cleaned up %d old version(s)\n", removed)
	}

	fmt.Printf("Updated to v%s\n", latestVersion)
}

// getLatestRelease fetches the latest release info from GitHub.
func getLatestRelease() (version string, downloadURL string) {
	client := &http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequest("GET", "https://api.github.com/repos/"+repo+"/releases/latest", nil)
	if err != nil {
		return "", ""
	}
	req.Header.Set("Accept", "application/vnd.github.v3+json")

	resp, err := client.Do(req)
	if err != nil || resp.StatusCode != 200 {
		return "", ""
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	var release struct {
		TagName string `json:"tag_name"`
		Assets  []struct {
			Name               string `json:"name"`
			BrowserDownloadURL string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.Unmarshal(body, &release); err != nil {
		return "", ""
	}

	version = strings.TrimPrefix(release.TagName, "v")

	// Find matching asset
	ext := ""
	if runtime.GOOS == "windows" {
		ext = ".exe"
	}
	assetName := fmt.Sprintf("claude2kiro-%s-%s%s", runtime.GOOS, runtime.GOARCH, ext)

	for _, asset := range release.Assets {
		if asset.Name == assetName {
			return version, asset.BrowserDownloadURL
		}
	}
	return version, ""
}

// downloadBinary downloads a URL to a file path.
func downloadBinary(url, targetPath string) error {
	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	return os.WriteFile(targetPath, data, 0755)
}

// downloadLatest downloads the latest release to the bin directory.
func downloadLatest(binDir string) error {
	version, url := getLatestRelease()
	if version == "" || url == "" {
		return fmt.Errorf("no release found for %s/%s", runtime.GOOS, runtime.GOARCH)
	}

	ext := ""
	if runtime.GOOS == "windows" {
		ext = ".exe"
	}

	targetPath := filepath.Join(binDir, fmt.Sprintf("claude2kiro-%s%s", version, ext))
	fmt.Fprintf(os.Stderr, "Downloading v%s...\n", version)

	if err := downloadBinary(url, targetPath); err != nil {
		return err
	}

	// Write current.txt
	currentFile := filepath.Join(binDir, "current.txt")
	os.WriteFile(currentFile, []byte(version), 0644)

	fmt.Fprintf(os.Stderr, "Installed v%s\n", version)
	return nil
}

// cleanOldVersions removes old versioned binaries, keeping the newest N.
func cleanOldVersions(binDir string, keep int) int {
	ext := ""
	if runtime.GOOS == "windows" {
		ext = ".exe"
	}

	entries, err := os.ReadDir(binDir)
	if err != nil {
		return 0
	}

	// Read current version to never delete it
	currentVersion := ""
	if data, err := os.ReadFile(filepath.Join(binDir, "current.txt")); err == nil {
		currentVersion = strings.TrimSpace(string(data))
	}

	var versions []string
	prefix := "claude2kiro-"
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, prefix) && !e.IsDir() {
			ver := strings.TrimPrefix(name, prefix)
			ver = strings.TrimSuffix(ver, ext)
			if ver != "" {
				versions = append(versions, ver)
			}
		}
	}

	if len(versions) <= keep {
		return 0
	}

	sort.Strings(versions)
	toDelete := versions[:len(versions)-keep]

	removed := 0
	for _, ver := range toDelete {
		if ver == currentVersion {
			continue // Never delete current
		}
		path := filepath.Join(binDir, fmt.Sprintf("claude2kiro-%s%s", ver, ext))
		if os.Remove(path) == nil {
			removed++
		}
	}
	return removed
}
