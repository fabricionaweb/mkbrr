package torrent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"
)

// TestCreateV2_TorrentStructure verifies that v2 torrents are created with correct structure
func TestCreateV2_TorrentStructure(t *testing.T) {
	// Create a temporary test file
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "test.txt")
	content := []byte("test content for v2 torrent creation")
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

	if torrent == nil {
		t.Fatal("CreateTorrent() returned nil torrent")
	}

	// Verify InfoBytes exist
	if len(torrent.MetaInfo.InfoBytes) == 0 {
		t.Error("v2 torrent should have InfoBytes")
	}

	// Unmarshal and verify v2-specific fields
	var info metainfo.Info
	if err := bencode.Unmarshal(torrent.MetaInfo.InfoBytes, &info); err != nil {
		t.Fatalf("Failed to unmarshal info: %v", err)
	}

	// Check MetaVersion
	if info.MetaVersion != 2 {
		t.Errorf("MetaVersion = %d, want 2", info.MetaVersion)
	}

	// Check FileTree exists (not Files or Length)
	if info.FileTree.Dir == nil && info.FileTree.File.Length == 0 {
		t.Error("v2 torrent should have FileTree")
	}

	// Check Pieces is empty (v2 doesn't use flat piece hashes)
	if len(info.Pieces) != 0 {
		t.Errorf("v2 torrent should not have Pieces field, got %d bytes", len(info.Pieces))
	}

	// Check Name
	if info.Name != "test.txt" {
		t.Errorf("Name = %q, want %q", info.Name, "test.txt")
	}

	// Check PieceLength is set and is power of 2
	if info.PieceLength == 0 {
		t.Error("PieceLength should be set")
	}
	// Verify power of 2
	if info.PieceLength&(info.PieceLength-1) != 0 {
		t.Errorf("PieceLength %d is not a power of 2", info.PieceLength)
	}

	// Check PiecesRoot is set in FileTree
	if info.FileTree.File.PiecesRoot == "" {
		t.Error("v2 single file torrent should have PiecesRoot in FileTree")
	}

	// Check Length matches
	if info.FileTree.File.Length != int64(len(content)) {
		t.Errorf("FileTree.File.Length = %d, want %d", info.FileTree.File.Length, len(content))
	}
}

// TestCreateV2_MultiFile tests v2 torrent creation with multiple files
func TestCreateV2_MultiFile(t *testing.T) {
	// Create a temporary directory with multiple files
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
		filepath.Join(contentDir, "file1.txt"): "content1",
		filepath.Join(contentDir, "file2.txt"): "content2",
		filepath.Join(subDir, "file3.txt"):     "content3",
	}

	for path, content := range files {
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatalf("Failed to create file %s: %v", path, err)
		}
	}

	opts := CreateOptions{
		Path:      contentDir,
		Name:      "test-multi",
		Format:    FormatV2,
		NoDate:    true,
		NoCreator: true,
	}

	torrent, err := CreateTorrent(opts)
	if err != nil {
		t.Fatalf("CreateTorrent() v2 error = %v", err)
	}

	// Unmarshal info
	var info metainfo.Info
	if err := bencode.Unmarshal(torrent.MetaInfo.InfoBytes, &info); err != nil {
		t.Fatalf("Failed to unmarshal info: %v", err)
	}

	// Check MetaVersion
	if info.MetaVersion != 2 {
		t.Errorf("MetaVersion = %d, want 2", info.MetaVersion)
	}

	// Check FileTree structure
	if info.FileTree.Dir == nil {
		t.Fatal("Multi-file v2 torrent should have FileTree.Dir")
	}

	// Check that files are in the tree
	if _, ok := info.FileTree.Dir["file1.txt"]; !ok {
		t.Error("file1.txt should be in FileTree")
	}
	if _, ok := info.FileTree.Dir["file2.txt"]; !ok {
		t.Error("file2.txt should be in FileTree")
	}
	if _, ok := info.FileTree.Dir["subdir"]; !ok {
		t.Error("subdir should be in FileTree")
	}

	// Check each file has PiecesRoot
	for filename := range files {
		baseName := filepath.Base(filename)
		if baseName == "file3.txt" {
			// This is in subdir
			if subdir, ok := info.FileTree.Dir["subdir"]; ok {
				if subdir.Dir == nil || subdir.Dir["file3.txt"].File.PiecesRoot == "" {
					t.Error("file3.txt in subdir should have PiecesRoot")
				}
			}
		} else {
			if info.FileTree.Dir[baseName].File.PiecesRoot == "" {
				t.Errorf("%s should have PiecesRoot", baseName)
			}
		}
	}

	// Verify Name
	if info.Name != "test-multi" {
		t.Errorf("Name = %q, want %q", info.Name, "test-multi")
	}
}

// TestCreateV2_LargeFile tests v2 torrent with file larger than piece length
func TestCreateV2_LargeFile(t *testing.T) {
	// Create a large file (256 KiB to ensure multiple pieces with small piece size)
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "large.bin")

	// Create 256 KiB file
	content := make([]byte, 256*1024)
	for i := range content {
		content[i] = byte(i % 256)
	}
	if err := os.WriteFile(testFile, content, 0644); err != nil {
		t.Fatalf("Failed to create large file: %v", err)
	}

	// Use 64 KiB piece length (2^16)
	pieceLengthExp := uint(16)
	opts := CreateOptions{
		Path:           testFile,
		Format:         FormatV2,
		PieceLengthExp: &pieceLengthExp,
		NoDate:         true,
		NoCreator:      true,
	}

	torrent, err := CreateTorrent(opts)
	if err != nil {
		t.Fatalf("CreateTorrent() v2 error = %v", err)
	}

	// Unmarshal info
	var info metainfo.Info
	if err := bencode.Unmarshal(torrent.MetaInfo.InfoBytes, &info); err != nil {
		t.Fatalf("Failed to unmarshal info: %v", err)
	}

	// Check that PieceLayers is set for large files
	if len(torrent.MetaInfo.PieceLayers) == 0 {
		t.Error("Large file v2 torrent should have PieceLayers")
	}

	// Verify the file has correct length
	if info.FileTree.File.Length != int64(len(content)) {
		t.Errorf("FileTree.File.Length = %d, want %d", info.FileTree.File.Length, len(content))
	}

	// Check PiecesRoot is set
	if info.FileTree.File.PiecesRoot == "" {
		t.Error("Large file should have PiecesRoot")
	}
}

// TestCreateV2_Symlink tests v2 torrent creation with symlinks
func TestCreateV2_Symlink(t *testing.T) {
	tmpDir := t.TempDir()

	// Create real content directory
	realContentDir := filepath.Join(tmpDir, "real_content")
	if err := os.MkdirAll(realContentDir, 0755); err != nil {
		t.Fatalf("Failed to create real content dir: %v", err)
	}

	// Create actual file with content
	realFile := filepath.Join(realContentDir, "file.txt")
	content := []byte("symlink test content for v2")
	if err := os.WriteFile(realFile, content, 0644); err != nil {
		t.Fatalf("Failed to create real file: %v", err)
	}

	// Create directory to contain the symlink
	linkDir := filepath.Join(tmpDir, "link_dir")
	if err := os.Mkdir(linkDir, 0755); err != nil {
		t.Fatalf("Failed to create link_dir: %v", err)
	}

	// Create the symlink pointing to the real file (relative path)
	linkPath := filepath.Join(linkDir, "link_to_file.txt")
	linkTarget := "../real_content/file.txt"
	if err := os.Symlink(linkTarget, linkPath); err != nil {
		t.Skipf("Skipping symlink test: %v", err)
	}

	opts := CreateOptions{
		Path:      linkDir,
		Format:    FormatV2,
		NoDate:    true,
		NoCreator: true,
	}

	torrent, err := CreateTorrent(opts)
	if err != nil {
		t.Fatalf("CreateTorrent() v2 with symlink error = %v", err)
	}

	// Unmarshal and verify
	var info metainfo.Info
	if err := bencode.Unmarshal(torrent.MetaInfo.InfoBytes, &info); err != nil {
		t.Fatalf("Failed to unmarshal info: %v", err)
	}

	// Check MetaVersion
	if info.MetaVersion != 2 {
		t.Errorf("MetaVersion = %d, want 2", info.MetaVersion)
	}

	// Verify file was included via symlink
	if info.FileTree.Dir == nil {
		t.Fatal("Symlinked directory should have FileTree.Dir")
	}

	if _, ok := info.FileTree.Dir["link_to_file.txt"]; !ok {
		t.Error("link_to_file.txt should be in FileTree (accessed via symlink)")
	}

	// Verify the file has correct length
	if info.FileTree.Dir["link_to_file.txt"].File.Length != int64(len(content)) {
		t.Errorf("File length = %d, want %d", info.FileTree.Dir["link_to_file.txt"].File.Length, len(content))
	}

	t.Logf("Symlink test successful: Torrent created from %q, correctly referencing content from %q", linkDir, realFile)
}

// TestCreateV2_Entropy tests v2 torrent creation with entropy field
func TestCreateV2_Entropy(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "test.txt")
	content := []byte("entropy test content")
	if err := os.WriteFile(testFile, content, 0644); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	opts := CreateOptions{
		Path:      testFile,
		Format:    FormatV2,
		Entropy:   true,
		NoDate:    true,
		NoCreator: true,
	}

	torrent, err := CreateTorrent(opts)
	if err != nil {
		t.Fatalf("CreateTorrent() v2 with entropy error = %v", err)
	}

	// Unmarshal info as map to check for entropy field
	var infoMap map[string]interface{}
	if err := bencode.Unmarshal(torrent.MetaInfo.InfoBytes, &infoMap); err != nil {
		t.Fatalf("Failed to unmarshal info map: %v", err)
	}

	// Check entropy field exists
	if _, ok := infoMap["entropy"]; !ok {
		t.Error("v2 torrent with --entropy should have entropy field in info dictionary")
	}

	// Also verify standard v2 fields
	var info metainfo.Info
	if err := bencode.Unmarshal(torrent.MetaInfo.InfoBytes, &info); err != nil {
		t.Fatalf("Failed to unmarshal info: %v", err)
	}

	if info.MetaVersion != 2 {
		t.Errorf("MetaVersion = %d, want 2", info.MetaVersion)
	}
}

// TestCreateV2_PieceLengthPowerOfTwo verifies that v2 torrents enforce power-of-2 piece lengths per BEP 52
func TestCreateV2_PieceLengthPowerOfTwo(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "test.bin")
	// Create 1 MiB file
	content := make([]byte, 1<<20)
	for i := range content {
		content[i] = byte(i % 256)
	}
	if err := os.WriteFile(testFile, content, 0644); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	tests := []struct {
		name           string
		pieceLengthExp uint
		shouldSucceed  bool
		description    string
	}{
		{
			name:           "power of 2 - 64 KiB",
			pieceLengthExp: 16, // 2^16 = 64 KiB
			shouldSucceed:  true,
			description:    "Valid power of 2",
		},
		{
			name:           "minimum allowed - 16 KiB",
			pieceLengthExp: 14, // 2^14 = 16 KiB (BEP 52 minimum)
			shouldSucceed:  true,
			description:    "BEP 52 minimum piece length",
		},
		{
			name:           "maximum allowed - 128 MiB",
			pieceLengthExp: 27, // 2^27 = 128 MiB (maximum)
			shouldSucceed:  true,
			description:    "maximum piece length",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := CreateOptions{
				Path:           testFile,
				Format:         FormatV2,
				PieceLengthExp: &tt.pieceLengthExp,
				NoDate:         true,
				NoCreator:      true,
			}

			torrent, err := CreateTorrent(opts)
			if tt.shouldSucceed {
				if err != nil {
					t.Fatalf("Expected success for %s, got error: %v", tt.description, err)
				}

				// Verify piece length is power of 2
				var info metainfo.Info
				if err := bencode.Unmarshal(torrent.MetaInfo.InfoBytes, &info); err != nil {
					t.Fatalf("Failed to unmarshal info: %v", err)
				}

				// Check power of 2: n & (n-1) == 0
				if info.PieceLength&(info.PieceLength-1) != 0 {
					t.Errorf("PieceLength %d is not a power of 2", info.PieceLength)
				}

				// Verify it's within range [14, 27]
				pieceLenExp := uint(0)
				for p := info.PieceLength; p > 1; p >>= 1 {
					pieceLenExp++
				}
				if pieceLenExp < 14 || pieceLenExp > 27 {
					t.Errorf("Piece length exponent %d is outside range [14, 27]", pieceLenExp)
				}
			}
		})
	}
}

// TestCreateV2_MixedFileSizes tests v2 torrent with mix of small, medium, and large files
// This validates BEP 52 per-file merkle tree logic for files with/without piece layers
func TestCreateV2_MixedFileSizes(t *testing.T) {
	tmpDir := t.TempDir()
	contentDir := filepath.Join(tmpDir, "mixed")
	if err := os.MkdirAll(contentDir, 0755); err != nil {
		t.Fatalf("Failed to create content dir: %v", err)
	}

	// Use 64 KiB piece length (2^16)
	pieceLength := int64(64 * 1024)

	// Create files of different sizes:
	// small.txt: 1 KiB (< piece length, no piece layers)
	// medium.txt: 64 KiB (= piece length, boundary case)
	// large.bin: 256 KiB (> piece length, has piece layers)
	files := map[string]struct {
		content   []byte
		hasLayers bool
	}{
		"small.txt":  {content: make([]byte, 1<<10), hasLayers: false},      // 1 KiB
		"medium.txt": {content: make([]byte, pieceLength), hasLayers: true}, // 64 KiB (one layer = root itself)
		"large.bin":  {content: make([]byte, 256<<10), hasLayers: true},     // 256 KiB (4 layers)
	}

	for name, f := range files {
		for i := range f.content {
			f.content[i] = byte(i % 256)
		}
		path := filepath.Join(contentDir, name)
		if err := os.WriteFile(path, f.content, 0644); err != nil {
			t.Fatalf("Failed to create file %s: %v", name, err)
		}
	}

	pieceLengthExp := uint(16)
	opts := CreateOptions{
		Path:           contentDir,
		Name:           "mixed-sizes",
		Format:         FormatV2,
		PieceLengthExp: &pieceLengthExp,
		NoDate:         true,
		NoCreator:      true,
	}

	torrent, err := CreateTorrent(opts)
	if err != nil {
		t.Fatalf("CreateTorrent() v2 error = %v", err)
	}

	// Unmarshal info
	var info metainfo.Info
	if err := bencode.Unmarshal(torrent.MetaInfo.InfoBytes, &info); err != nil {
		t.Fatalf("Failed to unmarshal info: %v", err)
	}

	// Verify MetaVersion
	if info.MetaVersion != 2 {
		t.Errorf("MetaVersion = %d, want 2", info.MetaVersion)
	}

	// Verify FileTree structure
	if info.FileTree.Dir == nil {
		t.Fatal("Multi-file v2 torrent should have FileTree.Dir")
	}

	// Check all files are present
	for name := range files {
		if _, ok := info.FileTree.Dir[name]; !ok {
			t.Errorf("%s should be in FileTree", name)
		}
	}

	// Check PiecesRoot for all files (always present in v2)
	for name := range files {
		if info.FileTree.Dir[name].File.PiecesRoot == "" {
			t.Errorf("%s should have PiecesRoot", name)
		}
	}

	// Check file lengths
	for name, f := range files {
		if info.FileTree.Dir[name].File.Length != int64(len(f.content)) {
			t.Errorf("%s length = %d, want %d", name, info.FileTree.Dir[name].File.Length, len(f.content))
		}
	}

	// Verify PieceLayers logic:
	// - Files <= piece length: no piece layers (root hash is sufficient)
	// - Files > piece length: has piece layers (one per piece after first)
	// For medium.txt (exactly piece length): 1 block, no layers needed
	// For large.bin (256 KiB with 64 KiB pieces): 4 pieces, 3 layers

	// Check that large.bin has piece layers entry
	largeRoot := info.FileTree.Dir["large.bin"].File.PiecesRoot
	if largeRoot == "" {
		t.Fatal("large.bin should have PiecesRoot")
	}

	// Verify piece layers exist for files larger than piece length
	if len(torrent.MetaInfo.PieceLayers) == 0 {
		t.Error("Torrent should have PieceLayers for multi-piece files")
	}

	// Verify the hash of the large file appears in piece layers
	if _, ok := torrent.MetaInfo.PieceLayers[largeRoot]; !ok {
		t.Error("large.bin's root hash should be a key in PieceLayers")
	}

	// Calculate expected number of piece layers for large.bin:
	// 256 KiB / 64 KiB = 4 pieces
	// Piece layers contains the merkle root of EACH piece's blocks
	// So we expect 4 piece root hashes concatenated together
	expectedLayers := 32 * 4 // 4 pieces * 32 bytes per SHA-256 hash
	actualLayers := len(torrent.MetaInfo.PieceLayers[largeRoot])
	if actualLayers != expectedLayers {
		t.Errorf("large.bin piece layers length = %d, want %d", actualLayers, expectedLayers)
	}

	// Verify small.txt has NO piece layers (1 KiB < 64 KiB piece length)
	smallRoot := info.FileTree.Dir["small.txt"].File.PiecesRoot
	if _, ok := torrent.MetaInfo.PieceLayers[smallRoot]; ok {
		t.Error("small.txt should NOT have piece layers (single-piece file)")
	}

	// Verify medium.txt (64 KiB = exactly one piece) also has no piece layers
	// Even though it's exactly one piece, the merkle tree root IS the piecesRoot
	mediumRoot := info.FileTree.Dir["medium.txt"].File.PiecesRoot
	if _, ok := torrent.MetaInfo.PieceLayers[mediumRoot]; ok {
		t.Error("medium.txt should NOT have piece layers (exactly one piece)")
	}
}
