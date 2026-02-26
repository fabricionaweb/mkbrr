package torrent

import (
	"os"
	"path/filepath"
	"testing"
)

// TestVerifyDataV2_SingleFile tests v2 verification with a single file
func TestVerifyDataV2_SingleFile(t *testing.T) {
	// Create test file
	tmpDir := t.TempDir()
	contentDir := filepath.Join(tmpDir, "content")
	if err := os.MkdirAll(contentDir, 0755); err != nil {
		t.Fatalf("Failed to create content dir: %v", err)
	}

	testFile := filepath.Join(contentDir, "test.txt")
	content := []byte("test content for v2 verification")
	if err := os.WriteFile(testFile, content, 0644); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	// Create v2 torrent
	opts := CreateOptions{
		Path:      contentDir,
		Format:    FormatV2,
		NoDate:    true,
		NoCreator: true,
	}

	torrent, err := CreateTorrent(opts)
	if err != nil {
		t.Fatalf("CreateTorrent() v2 error = %v", err)
	}

	// Save torrent file
	torrentPath := filepath.Join(tmpDir, "test.torrent")
	f, err := os.Create(torrentPath)
	if err != nil {
		t.Fatalf("Failed to create torrent file: %v", err)
	}
	if err := torrent.Write(f); err != nil {
		f.Close()
		t.Fatalf("Failed to write torrent: %v", err)
	}
	f.Close()

	// Verify against the same content
	verifyOpts := VerifyOptions{
		TorrentPath: torrentPath,
		ContentPath: contentDir,
		Verbose:     false,
		Quiet:       true,
	}

	result, err := VerifyData(verifyOpts)
	if err != nil {
		t.Fatalf("VerifyData() error = %v", err)
	}

	// Should be 100% complete
	if result.Completion != 100.0 {
		t.Errorf("Completion = %.2f%%, want 100%%", result.Completion)
	}

	if result.GoodPieces != 1 {
		t.Errorf("GoodPieces = %d, want 1", result.GoodPieces)
	}

	if result.BadPieces != 0 {
		t.Errorf("BadPieces = %d, want 0", result.BadPieces)
	}

	if len(result.MissingFiles) != 0 {
		t.Errorf("MissingFiles = %v, want empty", result.MissingFiles)
	}
}

// TestVerifyDataV2_MultiFile tests v2 verification with multiple files
func TestVerifyDataV2_MultiFile(t *testing.T) {
	// Create test directory with multiple files
	tmpDir := t.TempDir()
	contentDir := filepath.Join(tmpDir, "content")
	if err := os.MkdirAll(contentDir, 0755); err != nil {
		t.Fatalf("Failed to create content dir: %v", err)
	}

	// Create subdirectory
	subDir := filepath.Join(contentDir, "subdir")
	if err := os.MkdirAll(subDir, 0755); err != nil {
		t.Fatalf("Failed to create subdir: %v", err)
	}

	// Create files
	files := map[string]string{
		"file1.txt":        "content of file 1",
		"file2.txt":        "content of file 2",
		"subdir/file3.txt": "content of file 3 in subdir",
	}

	for path, content := range files {
		fullPath := filepath.Join(contentDir, path)
		if err := os.WriteFile(fullPath, []byte(content), 0644); err != nil {
			t.Fatalf("Failed to create file %s: %v", path, err)
		}
	}

	// Create v2 torrent
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

	// Save torrent file
	torrentPath := filepath.Join(tmpDir, "test.torrent")
	f, err := os.Create(torrentPath)
	if err != nil {
		t.Fatalf("Failed to create torrent file: %v", err)
	}
	if err := torrent.Write(f); err != nil {
		f.Close()
		t.Fatalf("Failed to write torrent: %v", err)
	}
	f.Close()

	// Verify against the same content
	verifyOpts := VerifyOptions{
		TorrentPath: torrentPath,
		ContentPath: contentDir,
		Verbose:     false,
		Quiet:       true,
	}

	result, err := VerifyData(verifyOpts)
	if err != nil {
		t.Fatalf("VerifyData() error = %v", err)
	}

	// Should be 100% complete
	if result.Completion != 100.0 {
		t.Errorf("Completion = %.2f%%, want 100%%", result.Completion)
	}

	if result.BadPieces != 0 {
		t.Errorf("BadPieces = %d, want 0", result.BadPieces)
	}

	if len(result.MissingFiles) != 0 {
		t.Errorf("MissingFiles = %v, want empty", result.MissingFiles)
	}
}

// TestVerifyDataV2_LargeFile tests v2 verification with a large multi-piece file
func TestVerifyDataV2_LargeFile(t *testing.T) {
	// Create test directory with a large file
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

	// Create v2 torrent with 64 KiB piece length (4 pieces)
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

	// Verify piece layers exist for multi-piece file
	if len(torrent.MetaInfo.PieceLayers) == 0 {
		t.Fatal("Large file v2 torrent should have PieceLayers")
	}

	// Save torrent file
	torrentPath := filepath.Join(tmpDir, "test.torrent")
	f, err := os.Create(torrentPath)
	if err != nil {
		t.Fatalf("Failed to create torrent file: %v", err)
	}
	if err := torrent.Write(f); err != nil {
		f.Close()
		t.Fatalf("Failed to write torrent: %v", err)
	}
	f.Close()

	// Verify against the same content
	verifyOpts := VerifyOptions{
		TorrentPath: torrentPath,
		ContentPath: contentDir,
		Verbose:     false,
		Quiet:       true,
	}

	result, err := VerifyData(verifyOpts)
	if err != nil {
		t.Fatalf("VerifyData() error = %v", err)
	}

	// Should be 100% complete
	if result.Completion != 100.0 {
		t.Errorf("Completion = %.2f%%, want 100%%", result.Completion)
	}

	if result.BadPieces != 0 {
		t.Errorf("BadPieces = %d, want 0", result.BadPieces)
	}
}

// TestVerifyDataV2_MissingFile tests v2 verification with missing files
func TestVerifyDataV2_MissingFile(t *testing.T) {
	// Create test directory with multiple files
	tmpDir := t.TempDir()
	contentDir := filepath.Join(tmpDir, "content")
	if err := os.MkdirAll(contentDir, 0755); err != nil {
		t.Fatalf("Failed to create content dir: %v", err)
	}

	// Create files
	if err := os.WriteFile(filepath.Join(contentDir, "file1.txt"), []byte("content1"), 0644); err != nil {
		t.Fatalf("Failed to create file1: %v", err)
	}
	if err := os.WriteFile(filepath.Join(contentDir, "file2.txt"), []byte("content2"), 0644); err != nil {
		t.Fatalf("Failed to create file2: %v", err)
	}

	// Create v2 torrent
	opts := CreateOptions{
		Path:      contentDir,
		Format:    FormatV2,
		NoDate:    true,
		NoCreator: true,
	}

	torrent, err := CreateTorrent(opts)
	if err != nil {
		t.Fatalf("CreateTorrent() v2 error = %v", err)
	}

	// Save torrent file
	torrentPath := filepath.Join(tmpDir, "test.torrent")
	f, err := os.Create(torrentPath)
	if err != nil {
		t.Fatalf("Failed to create torrent file: %v", err)
	}
	if err := torrent.Write(f); err != nil {
		f.Close()
		t.Fatalf("Failed to write torrent: %v", err)
	}
	f.Close()

	// Remove one file to simulate missing file
	if err := os.Remove(filepath.Join(contentDir, "file2.txt")); err != nil {
		t.Fatalf("Failed to remove file: %v", err)
	}

	// Verify against incomplete content
	verifyOpts := VerifyOptions{
		TorrentPath: torrentPath,
		ContentPath: contentDir,
		Verbose:     false,
		Quiet:       true,
	}

	result, err := VerifyData(verifyOpts)
	if err != nil {
		t.Fatalf("VerifyData() error = %v", err)
	}

	// Should have missing files
	if len(result.MissingFiles) == 0 {
		t.Error("Expected missing files, got none")
	}

	// Should not be 100% complete
	if result.Completion == 100.0 {
		t.Error("Expected incomplete verification, got 100%")
	}
}

// TestVerifyDataV2_CorruptedFile tests v2 verification with corrupted content
func TestVerifyDataV2_CorruptedFile(t *testing.T) {
	// Create test directory
	tmpDir := t.TempDir()
	contentDir := filepath.Join(tmpDir, "content")
	if err := os.MkdirAll(contentDir, 0755); err != nil {
		t.Fatalf("Failed to create content dir: %v", err)
	}

	// Create file
	testFile := filepath.Join(contentDir, "test.txt")
	if err := os.WriteFile(testFile, []byte("original content"), 0644); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	// Create v2 torrent
	opts := CreateOptions{
		Path:      contentDir,
		Format:    FormatV2,
		NoDate:    true,
		NoCreator: true,
	}

	torrent, err := CreateTorrent(opts)
	if err != nil {
		t.Fatalf("CreateTorrent() v2 error = %v", err)
	}

	// Save torrent file
	torrentPath := filepath.Join(tmpDir, "test.torrent")
	f, err := os.Create(torrentPath)
	if err != nil {
		t.Fatalf("Failed to create torrent file: %v", err)
	}
	if err := torrent.Write(f); err != nil {
		f.Close()
		t.Fatalf("Failed to write torrent: %v", err)
	}
	f.Close()

	// Corrupt the file (keep same length of 16 bytes)
	if err := os.WriteFile(testFile, []byte("CORRUPTEDCONtent"), 0644); err != nil {
		t.Fatalf("Failed to corrupt file: %v", err)
	}

	// Verify against corrupted content
	verifyOpts := VerifyOptions{
		TorrentPath: torrentPath,
		ContentPath: contentDir,
		Verbose:     false,
		Quiet:       true,
	}

	result, err := VerifyData(verifyOpts)
	if err != nil {
		t.Fatalf("VerifyData() error = %v", err)
	}

	// Should have bad pieces
	if result.BadPieces == 0 {
		t.Error("Expected bad pieces for corrupted file, got 0")
	}

	// Should not be 100% complete
	if result.Completion == 100.0 {
		t.Error("Expected incomplete verification for corrupted file, got 100%")
	}
}
