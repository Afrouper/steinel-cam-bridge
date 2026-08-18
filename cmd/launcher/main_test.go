package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"
)

func TestSetEnv(t *testing.T) {
	env := []string{"FOO=BAR", "PATH=/usr/bin"}

	// Update existing
	updated := setEnv(env, "FOO", "BAZ")
	found := false
	for _, e := range updated {
		if e == "FOO=BAZ" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected FOO=BAZ in updated env, got %v", updated)
	}

	// Add new
	added := setEnv(env, "NEW_KEY", "VALUE")
	foundNew := false
	for _, e := range added {
		if e == "NEW_KEY=VALUE" {
			foundNew = true
		}
	}
	if !foundNew {
		t.Errorf("expected NEW_KEY=VALUE in added env, got %v", added)
	}
}

func TestIsLibraryReady(t *testing.T) {
	tmpDir := t.TempDir()
	soPath := filepath.Join(tmpDir, "libnabto_client.so")
	verPath := filepath.Join(tmpDir, "installed_version.txt")

	// 1. Files do not exist
	if isLibraryReady(soPath, verPath, "5.15.4") {
		t.Errorf("expected isLibraryReady to return false when files do not exist")
	}

	// 2. File too small (< minSOSizeBytes)
	if err := os.WriteFile(soPath, []byte("short"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(verPath, []byte("5.15.4"), 0644); err != nil {
		t.Fatal(err)
	}
	if isLibraryReady(soPath, verPath, "5.15.4") {
		t.Errorf("expected isLibraryReady to return false when file size is < 1MB")
	}

	// 3. File size adequate, but version mismatch
	largeData := make([]byte, minSOSizeBytes+10)
	if err := os.WriteFile(soPath, largeData, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(verPath, []byte("5.14.0"), 0644); err != nil {
		t.Fatal(err)
	}
	if isLibraryReady(soPath, verPath, "5.15.4") {
		t.Errorf("expected isLibraryReady to return false on version mismatch")
	}

	// 4. File size adequate and version matches
	if err := os.WriteFile(verPath, []byte("5.15.4\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if !isLibraryReady(soPath, verPath, "5.15.4") {
		t.Errorf("expected isLibraryReady to return true when size and version match")
	}
}

func TestCleanupOldVersions(t *testing.T) {
	tmpDir := t.TempDir()
	oldDir1 := filepath.Join(tmpDir, "5.14.0")
	oldDir2 := filepath.Join(tmpDir, "5.15.0")
	currentDir := filepath.Join(tmpDir, "5.15.4")

	if err := os.Mkdir(oldDir1, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(oldDir2, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(currentDir, 0755); err != nil {
		t.Fatal(err)
	}

	cleanupOldVersions(tmpDir, "5.15.4")

	if _, err := os.Stat(oldDir1); !os.IsNotExist(err) {
		t.Errorf("expected oldDir1 to be deleted")
	}
	if _, err := os.Stat(oldDir2); !os.IsNotExist(err) {
		t.Errorf("expected oldDir2 to be deleted")
	}
	if _, err := os.Stat(currentDir); os.IsNotExist(err) {
		t.Errorf("expected currentDir to be preserved")
	}
}

func TestExtractSOFromReader(t *testing.T) {
	tmpDir := t.TempDir()
	destPath := filepath.Join(tmpDir, "extracted.so")

	// Create an in-memory tar.gz archive
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)

	dummyPayload := []byte("Nabto Shared Object Content Mock")
	header := &tar.Header{
		Name: "nabto-client-sdk-releases-5.15.4/lib/linux-x86_64/libnabto_client.so",
		Mode: 0755,
		Size: int64(len(dummyPayload)),
	}

	if err := tw.WriteHeader(header); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(dummyPayload); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gw.Close(); err != nil {
		t.Fatal(err)
	}

	// Extract using extractSOFromReader
	err := extractSOFromReader(&buf, destPath, "linux-x86_64")
	if err != nil {
		t.Fatalf("extractSOFromReader failed: %v", err)
	}

	content, err := os.ReadFile(destPath)
	if err != nil {
		t.Fatalf("failed to read extracted file: %v", err)
	}

	if string(content) != string(dummyPayload) {
		t.Errorf("expected '%s', got '%s'", string(dummyPayload), string(content))
	}
}
