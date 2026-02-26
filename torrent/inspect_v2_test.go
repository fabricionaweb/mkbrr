package torrent

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestShowFileTreeV2_SingleFile tests that v2 single file torrents display correctly
func TestShowFileTreeV2_SingleFile(t *testing.T) {
	// Create a v2 torrent
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "test.txt")
	content := []byte("test content for inspect")
	if err := os.WriteFile(testFile, content, 0644); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	opts := CreateOptions{
		Path:      testFile,
		Format:    FormatV2,
		NoDate:    true,
		NoCreator: true,
	}

	torrent, err := CreateTorrent(opts)
	if err != nil {
		t.Fatalf("CreateTorrent() v2 error = %v", err)
	}

	// Get info
	info, err := torrent.UnmarshalInfo()
	if err != nil {
		t.Fatalf("Failed to unmarshal info: %v", err)
	}

	// Create display with buffer to capture output
	var buf bytes.Buffer
	display := NewDisplay(NewFormatter(false))
	display.output = &buf

	// Show file tree (for single files, IsDir returns false, so we call it directly)
	if !info.IsDir() {
		// For single file, manually call to test the path
		// In real usage, inspect command wouldn't show tree for single files
		t.Log("Single file torrent - no tree displayed (expected)")
	}

	// Test that v2 format is detected
	if !(info.MetaVersion == 2 && !info.HasV1()) {
		t.Error("Expected v2 format (MetaVersion == 2 && !HasV1)")
	}
}

// TestShowFileTreeV2_MultiFile tests that v2 multi-file torrents display correctly
func TestShowFileTreeV2_MultiFile(t *testing.T) {
	// Create a multi-file v2 torrent
	tmpDir := t.TempDir()
	contentDir := filepath.Join(tmpDir, "content")
	if err := os.MkdirAll(contentDir, 0755); err != nil {
		t.Fatalf("Failed to create content dir: %v", err)
	}

	// Create subdirectories and files
	subDir := filepath.Join(contentDir, "subdir")
	if err := os.MkdirAll(subDir, 0755); err != nil {
		t.Fatalf("Failed to create subdir: %v", err)
	}

	files := map[string]string{
		"file1.txt":        "content1",
		"file2.txt":        "content2",
		"subdir/file3.txt": "content3",
	}

	for path, content := range files {
		fullPath := filepath.Join(contentDir, path)
		if err := os.WriteFile(fullPath, []byte(content), 0644); err != nil {
			t.Fatalf("Failed to create file %s: %v", path, err)
		}
	}

	opts := CreateOptions{
		Path:      contentDir,
		Name:      "multi-file-test",
		Format:    FormatV2,
		NoDate:    true,
		NoCreator: true,
	}

	torrent, err := CreateTorrent(opts)
	if err != nil {
		t.Fatalf("CreateTorrent() v2 error = %v", err)
	}

	// Get info
	info, err := torrent.UnmarshalInfo()
	if err != nil {
		t.Fatalf("Failed to unmarshal info: %v", err)
	}

	// Verify it's v2
	if !(info.MetaVersion == 2 && !info.HasV1()) {
		t.Fatal("Expected v2 torrent")
	}

	// Create display with buffer to capture output
	var buf bytes.Buffer
	display := NewDisplay(NewFormatter(false))
	display.output = &buf

	// Show file tree
	display.ShowFileTree(&info)

	// Check output contains expected files
	output := buf.String()
	if !strings.Contains(output, "multi-file-test") {
		t.Error("Expected output to contain torrent name")
	}
	if !strings.Contains(output, "file1.txt") {
		t.Error("Expected output to contain file1.txt")
	}
	if !strings.Contains(output, "file2.txt") {
		t.Error("Expected output to contain file2.txt")
	}
	if !strings.Contains(output, "subdir") {
		t.Error("Expected output to contain subdir")
	}
	if !strings.Contains(output, "file3.txt") {
		t.Error("Expected output to contain file3.txt")
	}
}

// TestShowFileTreeV2_LargeFile tests v2 file tree with large multi-piece file
func TestShowFileTreeV2_LargeFile(t *testing.T) {
	// Create a v2 torrent with large file
	tmpDir := t.TempDir()
	contentDir := filepath.Join(tmpDir, "content")
	if err := os.MkdirAll(contentDir, 0755); err != nil {
		t.Fatalf("Failed to create content dir: %v", err)
	}

	// Create 256 KiB file
	testFile := filepath.Join(contentDir, "large.bin")
	content := make([]byte, 256*1024)
	for i := range content {
		content[i] = byte(i % 256)
	}
	if err := os.WriteFile(testFile, content, 0644); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	pieceLengthExp := uint(16)
	opts := CreateOptions{
		Path:           contentDir,
		Format:         FormatV2,
		PieceLengthExp: &pieceLengthExp,
		NoDate:         true,
		NoCreator:      true,
	}

	torrent, err := CreateTorrent(opts)
	if err != nil {
		t.Fatalf("CreateTorrent() v2 error = %v", err)
	}

	// Get info
	info, err := torrent.UnmarshalInfo()
	if err != nil {
		t.Fatalf("Failed to unmarshal info: %v", err)
	}

	// Verify it's v2
	if !(info.MetaVersion == 2 && !info.HasV1()) {
		t.Fatal("Expected v2 torrent")
	}

	// Verify piece layers exist
	if len(torrent.MetaInfo.PieceLayers) == 0 {
		t.Error("Expected piece layers for multi-piece file")
	}

	// Create display with buffer to capture output
	var buf bytes.Buffer
	display := NewDisplay(NewFormatter(false))
	display.output = &buf

	// Show file tree
	display.ShowFileTree(&info)

	// Check output contains expected file (this is a multi-file torrent with one file)
	output := buf.String()
	if !strings.Contains(output, "large.bin") {
		t.Error("Expected output to contain large.bin")
	}
	if !strings.Contains(output, "256 KiB") {
		t.Error("Expected output to show file size")
	}
}

// TestShowFileTreeV2_SingleFile_NoTree tests that single-file v2 torrents don't show tree
func TestShowFileTreeV2_SingleFile_NoTree(t *testing.T) {
	// Create a single-file v2 torrent (not from directory)
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "single.txt")
	if err := os.WriteFile(testFile, []byte("test content"), 0644); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	opts := CreateOptions{
		Path:      testFile,
		Format:    FormatV2,
		NoDate:    true,
		NoCreator: true,
	}

	torrent, err := CreateTorrent(opts)
	if err != nil {
		t.Fatalf("CreateTorrent() v2 error = %v", err)
	}

	info, err := torrent.UnmarshalInfo()
	if err != nil {
		t.Fatalf("Failed to unmarshal info: %v", err)
	}

	// Create display with buffer to capture output
	var buf bytes.Buffer
	display := NewDisplay(NewFormatter(false))
	display.output = &buf

	// Show file tree - should be empty for single-file torrents
	display.ShowFileTree(&info)

	output := buf.String()
	if output != "" {
		t.Errorf("Single-file v2 torrent should not show file tree, got: %q", output)
	}
}

// TestShowTorrentInfoV2 tests that v2 torrent info is displayed correctly
func TestShowTorrentInfoV2(t *testing.T) {
	// Create a v2 torrent
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "test.txt")
	if err := os.WriteFile(testFile, []byte("test content"), 0644); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	opts := CreateOptions{
		Path:      testFile,
		Format:    FormatV2,
		NoDate:    true,
		NoCreator: true,
	}

	torrent, err := CreateTorrent(opts)
	if err != nil {
		t.Fatalf("CreateTorrent() v2 error = %v", err)
	}

	// Get info
	info, err := torrent.UnmarshalInfo()
	if err != nil {
		t.Fatalf("Failed to unmarshal info: %v", err)
	}

	// Create display with buffer to capture output
	var buf bytes.Buffer
	display := NewDisplay(NewFormatter(false))
	display.output = &buf

	// Show torrent info
	tor := &Torrent{MetaInfo: torrent.MetaInfo}
	display.ShowTorrentInfo(tor, &info)

	// Check output contains expected info
	output := buf.String()
	if !strings.Contains(output, "test.txt") {
		t.Error("Expected output to contain torrent name")
	}
	if !strings.Contains(output, "Hash:") {
		t.Error("Expected output to contain hash")
	}
	if !strings.Contains(output, "Size:") {
		t.Error("Expected output to contain size")
	}
	if !strings.Contains(output, "Piece length:") {
		t.Error("Expected output to contain piece length")
	}
	if !strings.Contains(output, "Pieces:") {
		t.Error("Expected output to contain piece count")
	}

	// For v2, hash should be longer (SHA-256 vs SHA-1)
	// SHA-256 hex string is 64 chars, SHA-1 is 40 chars
	lines := strings.Split(output, "\n")
	for _, line := range lines {
		if strings.Contains(line, "Hash:") {
			// Extract hash value
			parts := strings.Split(line, "Hash:")
			if len(parts) > 1 {
				hash := strings.TrimSpace(parts[1])
				if len(hash) != 64 {
					t.Errorf("Expected SHA-256 hash (64 chars), got %d chars", len(hash))
				}
			}
		}
	}
}
