package main

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"
)

const (
	defaultSDKVersion = "5.15.4"
	defaultDataDir    = "/data"
	defaultBridgeBin  = "/app/steinel-bridge"
	minSOSizeBytes    = 1_000_000 // 1 MB minimum size threshold for valid binary
)

func main() {
	sdkVersion := os.Getenv("NABTO_SDK_VERSION")
	if sdkVersion == "" {
		sdkVersion = defaultSDKVersion
	}

	dataDir := os.Getenv("DATA_DIR")
	if dataDir == "" {
		dataDir = defaultDataDir
	}

	bridgeBin := os.Getenv("BRIDGE_BIN_PATH")
	if bridgeBin == "" {
		bridgeBin = defaultBridgeBin
	}

	targetDir := filepath.Join(dataDir, "lib", sdkVersion)
	targetSO := filepath.Join(targetDir, "libnabto_client.so")
	versionFile := filepath.Join(dataDir, "lib", "installed_version.txt")

	// 1. Detect Host Architecture
	var nabtoArch string
	switch runtime.GOARCH {
	case "amd64":
		nabtoArch = "linux-x86_64"
	case "arm64":
		nabtoArch = "linux-aarch64"
	default:
		log.Fatalf("[Nabto Setup] ❌ Error: Unsupported CPU architecture '%s'. Supported: amd64, arm64", runtime.GOARCH)
	}

	// 2. Check if library already exists and matches expected version
	if !isLibraryReady(targetSO, versionFile, sdkVersion) {
		log.Printf("[Nabto Setup] 🔄 Initializing Nabto Edge Client SDK v%s (%s)...", sdkVersion, nabtoArch)
		if err := downloadAndExtractSO(targetDir, targetSO, versionFile, sdkVersion, nabtoArch); err != nil {
			log.Fatalf("[Nabto Setup] ❌ Error installing SDK: %v", err)
		}
		cleanupOldVersions(filepath.Join(dataDir, "lib"), sdkVersion)
	}

	// 3. Export library path for dynamic linker
	existingLD := os.Getenv("LD_LIBRARY_PATH")
	newLD := fmt.Sprintf("%s:%s/lib:/usr/lib", targetDir, dataDir)
	if existingLD != "" {
		newLD = fmt.Sprintf("%s:%s", newLD, existingLD)
	}

	env := os.Environ()
	env = setEnv(env, "LD_LIBRARY_PATH", newLD)

	// 4. Replace launcher process with steinel-bridge via execve(2) (PID 1 preservation)
	args := append([]string{bridgeBin}, os.Args[1:]...)
	if err := syscall.Exec(bridgeBin, args, env); err != nil {
		log.Fatalf("[Nabto Setup] ❌ Failed to exec '%s': %v", bridgeBin, err)
	}
}

func isLibraryReady(soPath, versionFilePath, expectedVersion string) bool {
	fi, err := os.Stat(soPath)
	if err != nil || fi.Size() < minSOSizeBytes {
		return false
	}
	verBytes, err := os.ReadFile(versionFilePath)
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(verBytes)) == expectedVersion
}

func downloadAndExtractSO(targetDir, targetSO, versionFile, sdkVersion, nabtoArch string) error {
	if err := os.MkdirAll(targetDir, 0755); err != nil {
		return fmt.Errorf("cannot create directory %s: %w", targetDir, err)
	}

	// Place temporary file directly on target volume to ensure 0 byte disk usage in /tmp
	tmpSO := targetSO + ".tmp"
	_ = os.Remove(tmpSO)

	urls := []string{
		fmt.Sprintf("https://github.com/nabto/nabto-client-sdk-releases/archive/refs/tags/v%s.tar.gz", sdkVersion),
		"https://github.com/nabto/nabto-client-sdk-releases/archive/refs/heads/main.tar.gz",
	}

	client := &http.Client{
		Timeout: 120 * time.Second,
	}

	var lastErr error
	for _, url := range urls {
		log.Printf("[Nabto Setup] ⬇️ Streaming Nabto SDK v%s directly from GitHub (%s)...", sdkVersion, url)
		if err := fetchAndExtract(client, url, tmpSO, nabtoArch); err == nil {
			lastErr = nil
			break
		} else {
			lastErr = err
			_ = os.Remove(tmpSO)
		}
	}

	if lastErr != nil {
		return fmt.Errorf("failed to download libnabto_client.so from GitHub: %w", lastErr)
	}

	fi, err := os.Stat(tmpSO)
	if err != nil || fi.Size() < minSOSizeBytes {
		_ = os.Remove(tmpSO)
		return fmt.Errorf("downloaded library is invalid or corrupt (size: %d bytes)", fi.Size())
	}

	// Atomically move verified library to final destination
	if err := os.Rename(tmpSO, targetSO); err != nil {
		return fmt.Errorf("failed to finalize library: %w", err)
	}
	if err := os.Chmod(targetSO, 0755); err != nil {
		log.Printf("[Nabto Setup] ⚠️ Warning: Failed to set chmod 0755 on library: %v", err)
	}

	if err := os.WriteFile(versionFile, []byte(sdkVersion), 0644); err != nil {
		log.Printf("[Nabto Setup] ⚠️ Warning: Failed to write version file: %v", err)
	}

	log.Printf("[Nabto Setup] ✅ Nabto Client SDK v%s installed successfully (%d bytes).", sdkVersion, fi.Size())
	return nil
}

func fetchAndExtract(client *http.Client, url, destPath, nabtoArch string) error {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "steinel-cam-bridge-launcher")

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, resp.Status)
	}

	return extractSOFromReader(resp.Body, destPath, nabtoArch)
}

func extractSOFromReader(r io.Reader, destPath, nabtoArch string) error {
	gzr, err := gzip.NewReader(r)
	if err != nil {
		return fmt.Errorf("gzip reader error: %w", err)
	}
	defer func() { _ = gzr.Close() }()

	tr := tar.NewReader(gzr)
	targetSuffix := fmt.Sprintf("/lib/%s/libnabto_client.so", nabtoArch)

	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("tar read error: %w", err)
		}

		if strings.HasSuffix(header.Name, targetSuffix) {
			out, err := os.OpenFile(destPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0755)
			if err != nil {
				return fmt.Errorf("cannot create target file %s: %w", destPath, err)
			}

			_, copyErr := io.Copy(out, tr)
			closeErr := out.Close()
			if copyErr != nil {
				return fmt.Errorf("error streaming library to file: %w", copyErr)
			}
			if closeErr != nil {
				return fmt.Errorf("error closing target file: %w", closeErr)
			}
			return nil
		}
	}

	return fmt.Errorf("file with suffix '%s' not found in archive", targetSuffix)
}

func cleanupOldVersions(baseLibDir, currentVersion string) {
	entries, err := os.ReadDir(baseLibDir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.IsDir() && entry.Name() != currentVersion {
			_ = os.RemoveAll(filepath.Join(baseLibDir, entry.Name()))
		}
	}
}

func setEnv(environ []string, key, value string) []string {
	prefix := key + "="
	for i, env := range environ {
		if strings.HasPrefix(env, prefix) {
			environ[i] = prefix + value
			return environ
		}
	}
	return append(environ, prefix+value)
}
