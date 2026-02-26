package torrent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"
)

// TestModifyTorrentV2_PreservesPieceLayers tests that modifying a v2 torrent preserves piece layers
func TestModifyTorrentV2_PreservesPieceLayers(t *testing.T) {
	// Create a v2 torrent with a large file to ensure piece layers exist
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

	// Create v2 torrent with 64 KiB piece length
	pieceLengthExp := uint(16)
	createOpts := CreateOptions{
		Path:           contentDir,
		Format:         FormatV2,
		PieceLengthExp: &pieceLengthExp,
		NoDate:         true,
		NoCreator:      true,
	}

	torrent, err := CreateTorrent(createOpts)
	if err != nil {
		t.Fatalf("CreateTorrent() v2 error = %v", err)
	}

	// Verify piece layers exist
	if len(torrent.MetaInfo.PieceLayers) == 0 {
		t.Fatal("Expected piece layers in v2 torrent")
	}
	originalPieceLayers := len(torrent.MetaInfo.PieceLayers)

	// Save original torrent
	originalPath := filepath.Join(tmpDir, "original.torrent")
	f, err := os.Create(originalPath)
	if err != nil {
		t.Fatalf("Failed to create torrent file: %v", err)
	}
	if err := torrent.Write(f); err != nil {
		f.Close()
		t.Fatalf("Failed to write torrent: %v", err)
	}
	f.Close()

	// Modify the torrent (add comment)
	modifyOpts := ModifyOptions{
		Comment: "Modified v2 torrent",
		NoDate:  true,
	}

	result, err := ModifyTorrent(originalPath, modifyOpts)
	if err != nil {
		t.Fatalf("ModifyTorrent() error = %v", err)
	}

	if !result.WasModified {
		t.Fatal("Expected torrent to be modified")
	}

	// Load modified torrent
	modifiedMi, err := metainfo.LoadFromFile(result.OutputPath)
	if err != nil {
		t.Fatalf("Failed to load modified torrent: %v", err)
	}

	// Verify piece layers are preserved
	if len(modifiedMi.PieceLayers) != originalPieceLayers {
		t.Errorf("Piece layers count changed: got %d, want %d", len(modifiedMi.PieceLayers), originalPieceLayers)
	}

	// Verify comment was added
	if modifiedMi.Comment != "Modified v2 torrent" {
		t.Errorf("Comment = %q, want %q", modifiedMi.Comment, "Modified v2 torrent")
	}

	// Verify it's still v2
	info, err := modifiedMi.UnmarshalInfo()
	if err != nil {
		t.Fatalf("Failed to unmarshal info: %v", err)
	}

	if info.MetaVersion != 2 {
		t.Errorf("MetaVersion = %d, want 2", info.MetaVersion)
	}

	// Verify FileTree is preserved by checking it still has the file
	if info.FileTree.Dir == nil {
		t.Fatal("FileTree.Dir is nil after modification")
	}
	if _, ok := info.FileTree.Dir["large.bin"]; !ok {
		t.Fatal("large.bin not found in FileTree after modification")
	}
}

// TestModifyTorrentV2_Entropy tests that entropy can be added to v2 torrents
func TestModifyTorrentV2_Entropy(t *testing.T) {
	// Create a simple v2 torrent
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "test.txt")
	if err := os.WriteFile(testFile, []byte("test content"), 0644); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	createOpts := CreateOptions{
		Path:      testFile,
		Format:    FormatV2,
		NoDate:    true,
		NoCreator: true,
	}

	torrent, err := CreateTorrent(createOpts)
	if err != nil {
		t.Fatalf("CreateTorrent() v2 error = %v", err)
	}

	// Save original torrent
	originalPath := filepath.Join(tmpDir, "original.torrent")
	f, err := os.Create(originalPath)
	if err != nil {
		t.Fatalf("Failed to create torrent file: %v", err)
	}
	if err := torrent.Write(f); err != nil {
		f.Close()
		t.Fatalf("Failed to write torrent: %v", err)
	}
	f.Close()

	// Modify the torrent (add entropy)
	modifyOpts := ModifyOptions{
		Entropy: true,
		NoDate:  true,
	}

	result, err := ModifyTorrent(originalPath, modifyOpts)
	if err != nil {
		t.Fatalf("ModifyTorrent() error = %v", err)
	}

	if !result.WasModified {
		t.Fatal("Expected torrent to be modified")
	}

	// Load modified torrent
	modifiedMi, err := metainfo.LoadFromFile(result.OutputPath)
	if err != nil {
		t.Fatalf("Failed to load modified torrent: %v", err)
	}

	// Verify entropy field exists in info dict
	var infoMap map[string]interface{}
	if err := bencode.Unmarshal(modifiedMi.InfoBytes, &infoMap); err != nil {
		t.Fatalf("Failed to unmarshal info: %v", err)
	}

	if _, ok := infoMap["entropy"]; !ok {
		t.Error("Expected entropy field in info dictionary")
	}

	// Verify it's still v2
	info, err := modifiedMi.UnmarshalInfo()
	if err != nil {
		t.Fatalf("Failed to unmarshal info: %v", err)
	}

	if info.MetaVersion != 2 {
		t.Errorf("MetaVersion = %d, want 2", info.MetaVersion)
	}

	// Verify FileTree is preserved
	if info.FileTree.Dir == nil {
		t.Fatal("FileTree.Dir is nil after modification")
	}
	if _, ok := info.FileTree.Dir["test.txt"]; !ok {
		t.Fatal("test.txt not found in FileTree after modification")
	}
}

// TestModifyTorrentV2_UpdatePrivate tests updating private flag on v2 torrents
func TestModifyTorrentV2_UpdatePrivate(t *testing.T) {
	// Create a simple v2 torrent
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "test.txt")
	if err := os.WriteFile(testFile, []byte("test content"), 0644); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	createOpts := CreateOptions{
		Path:      testFile,
		Format:    FormatV2,
		NoDate:    true,
		NoCreator: true,
	}

	torrent, err := CreateTorrent(createOpts)
	if err != nil {
		t.Fatalf("CreateTorrent() v2 error = %v", err)
	}

	// Save original torrent
	originalPath := filepath.Join(tmpDir, "original.torrent")
	f, err := os.Create(originalPath)
	if err != nil {
		t.Fatalf("Failed to create torrent file: %v", err)
	}
	if err := torrent.Write(f); err != nil {
		f.Close()
		t.Fatalf("Failed to write torrent: %v", err)
	}
	f.Close()

	// Modify the torrent (set private)
	private := true
	modifyOpts := ModifyOptions{
		IsPrivate: &private,
		NoDate:    true,
	}

	result, err := ModifyTorrent(originalPath, modifyOpts)
	if err != nil {
		t.Fatalf("ModifyTorrent() error = %v", err)
	}

	if !result.WasModified {
		t.Fatal("Expected torrent to be modified")
	}

	// Verify the output file exists
	if _, err := os.Stat(result.OutputPath); err != nil {
		t.Fatalf("Modified torrent file doesn't exist: %v", err)
	}

	// Load modified torrent and verify private flag is set
	modifiedMi, err := metainfo.LoadFromFile(result.OutputPath)
	if err != nil {
		t.Fatalf("Failed to load modified torrent: %v", err)
	}

	var infoMap map[string]interface{}
	if err := bencode.Unmarshal(modifiedMi.InfoBytes, &infoMap); err != nil {
		t.Fatalf("Failed to unmarshal info: %v", err)
	}

	privateVal, ok := infoMap["private"]
	if !ok {
		t.Fatal("Private field not found in info dictionary")
	}

	// Check that private is set to 1
	if val, ok := privateVal.(int64); !ok || val != 1 {
		t.Errorf("Private = %v, want 1", privateVal)
	}

	// Verify FileTree is preserved
	info, err := modifiedMi.UnmarshalInfo()
	if err != nil {
		t.Fatalf("Failed to unmarshal info: %v", err)
	}

	if info.FileTree.Dir == nil {
		t.Fatal("FileTree.Dir is nil after modification")
	}
	if _, ok := info.FileTree.Dir["test.txt"]; !ok {
		t.Fatal("test.txt not found in FileTree after modification")
	}
}

// TestModifyTorrentV2_UpdateSource tests updating source field on v2 torrents
func TestModifyTorrentV2_UpdateSource(t *testing.T) {
	// Create a simple v2 torrent
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "test.txt")
	if err := os.WriteFile(testFile, []byte("test content"), 0644); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	createOpts := CreateOptions{
		Path:      testFile,
		Format:    FormatV2,
		NoDate:    true,
		NoCreator: true,
	}

	torrent, err := CreateTorrent(createOpts)
	if err != nil {
		t.Fatalf("CreateTorrent() v2 error = %v", err)
	}

	// Save original torrent
	originalPath := filepath.Join(tmpDir, "original.torrent")
	f, err := os.Create(originalPath)
	if err != nil {
		t.Fatalf("Failed to create torrent file: %v", err)
	}
	if err := torrent.Write(f); err != nil {
		f.Close()
		t.Fatalf("Failed to write torrent: %v", err)
	}
	f.Close()

	// Modify the torrent (set source)
	modifyOpts := ModifyOptions{
		Source: "MyTracker",
		NoDate: true,
	}

	result, err := ModifyTorrent(originalPath, modifyOpts)
	if err != nil {
		t.Fatalf("ModifyTorrent() error = %v", err)
	}

	if !result.WasModified {
		t.Fatal("Expected torrent to be modified")
	}

	// Load modified torrent and verify source is set
	modifiedMi, err := metainfo.LoadFromFile(result.OutputPath)
	if err != nil {
		t.Fatalf("Failed to load modified torrent: %v", err)
	}

	var infoMap map[string]interface{}
	if err := bencode.Unmarshal(modifiedMi.InfoBytes, &infoMap); err != nil {
		t.Fatalf("Failed to unmarshal info: %v", err)
	}

	sourceVal, ok := infoMap["source"]
	if !ok {
		t.Fatal("Source field not found in info dictionary")
	}

	if sourceVal != "MyTracker" {
		t.Errorf("Source = %v, want MyTracker", sourceVal)
	}

	// Verify FileTree is preserved
	info, err := modifiedMi.UnmarshalInfo()
	if err != nil {
		t.Fatalf("Failed to unmarshal info: %v", err)
	}

	if info.FileTree.Dir == nil {
		t.Fatal("FileTree.Dir is nil after modification")
	}
	if _, ok := info.FileTree.Dir["test.txt"]; !ok {
		t.Fatal("test.txt not found in FileTree after modification")
	}
}

// TestModifyTorrentV2_MultiFile tests modifying a multi-file v2 torrent
func TestModifyTorrentV2_MultiFile(t *testing.T) {
	// Create a multi-file v2 torrent
	tmpDir := t.TempDir()
	contentDir := filepath.Join(tmpDir, "content")
	if err := os.MkdirAll(contentDir, 0755); err != nil {
		t.Fatalf("Failed to create content dir: %v", err)
	}

	// Create multiple files
	files := map[string]string{
		"file1.txt": "content1",
		"file2.txt": "content2",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(contentDir, name), []byte(content), 0644); err != nil {
			t.Fatalf("Failed to create %s: %v", name, err)
		}
	}

	createOpts := CreateOptions{
		Path:      contentDir,
		Format:    FormatV2,
		NoDate:    true,
		NoCreator: true,
	}

	torrent, err := CreateTorrent(createOpts)
	if err != nil {
		t.Fatalf("CreateTorrent() v2 error = %v", err)
	}

	// Save original torrent
	originalPath := filepath.Join(tmpDir, "original.torrent")
	f, err := os.Create(originalPath)
	if err != nil {
		t.Fatalf("Failed to create torrent file: %v", err)
	}
	if err := torrent.Write(f); err != nil {
		f.Close()
		t.Fatalf("Failed to write torrent: %v", err)
	}
	f.Close()

	// Modify with multiple options
	private := true
	modifyOpts := ModifyOptions{
		Comment:   "Multi-file v2",
		Source:    "TestSource",
		IsPrivate: &private,
		NoDate:    true,
	}

	result, err := ModifyTorrent(originalPath, modifyOpts)
	if err != nil {
		t.Fatalf("ModifyTorrent() error = %v", err)
	}

	if !result.WasModified {
		t.Fatal("Expected torrent to be modified")
	}

	// Load modified torrent
	modifiedMi, err := metainfo.LoadFromFile(result.OutputPath)
	if err != nil {
		t.Fatalf("Failed to load modified torrent: %v", err)
	}

	// Verify all modifications
	if modifiedMi.Comment != "Multi-file v2" {
		t.Errorf("Comment = %q, want Multi-file v2", modifiedMi.Comment)
	}

	var infoMap map[string]interface{}
	if err := bencode.Unmarshal(modifiedMi.InfoBytes, &infoMap); err != nil {
		t.Fatalf("Failed to unmarshal info: %v", err)
	}

	if infoMap["source"] != "TestSource" {
		t.Errorf("Source = %v, want TestSource", infoMap["source"])
	}

	if privateVal, ok := infoMap["private"]; !ok || privateVal != int64(1) {
		t.Errorf("Private = %v, want 1", privateVal)
	}

	// Verify FileTree still has both files
	info, err := modifiedMi.UnmarshalInfo()
	if err != nil {
		t.Fatalf("Failed to unmarshal info: %v", err)
	}

	if info.FileTree.Dir == nil {
		t.Fatal("FileTree.Dir is nil after modification")
	}
	for name := range files {
		if _, ok := info.FileTree.Dir[name]; !ok {
			t.Errorf("%s not found in FileTree after modification", name)
		}
	}
}
