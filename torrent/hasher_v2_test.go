package torrent

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/anacrolix/torrent/merkle"
)

// TestFileHasher_Concurrent tests the file hasher with various scenarios
func TestFileHasher_Concurrent(t *testing.T) {
	tests := []struct {
		name     string
		numFiles int
		fileSize int64
	}{
		{
			name:     "single small file",
			numFiles: 1,
			fileSize: 1 << 20, // 1MB
		},
		{
			name:     "multi-file album",
			numFiles: 12,
			fileSize: 40 << 20, // 40MB per track
		},
		{
			name:     "single large file",
			numFiles: 1,
			fileSize: 256 << 20, // 256MB
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			totalSize := tt.fileSize * int64(tt.numFiles)
			if totalSize > 1<<30 {
				if runtime.GOOS == "windows" {
					t.Skipf("skipping large file test %s on Windows", tt.name)
				}
				if testing.Short() {
					t.Skipf("skipping large file test %s in short mode", tt.name)
				}
			}

			files, expectedHashes := createTestFilesV2(t, tt.numFiles, tt.fileSize)
			pieceLength := int64(1 << 16) // 64KB

			// Test with different worker counts
			workerCounts := []int{1, 2, 4}
			for _, workers := range workerCounts {
				t.Run(fmt.Sprintf("workers_%d", workers), func(t *testing.T) {
					hasher := NewFileHasher(files, pieceLength, &mockDisplay{}, workers, false)
					if err := hasher.hashFiles(); err != nil {
						t.Fatalf("hashFiles failed with %d workers: %v", workers, err)
					}
					verifyFileHashes(t, hasher.results, expectedHashes)
				})
			}
		})
	}
}

// createTestFilesV2 creates test files for v2 hashing and returns expected hashes
func createTestFilesV2(t *testing.T, numFiles int, fileSize int64) ([]fileEntry, [][32]byte) {
	t.Helper()

	tmpDir := t.TempDir()
	files := make([]fileEntry, numFiles)
	expectedHashes := make([][32]byte, numFiles)

	for i := 0; i < numFiles; i++ {
		path := filepath.Join(tmpDir, fmt.Sprintf("file_%d.dat", i))

		// Create file with deterministic content
		content := make([]byte, fileSize)
		for j := range content {
			content[j] = byte((j + i) % 256)
		}

		if err := os.WriteFile(path, content, 0644); err != nil {
			t.Fatalf("Failed to create test file: %v", err)
		}

		files[i] = fileEntry{
			path:   path,
			length: fileSize,
		}

		// Calculate expected merkle root
		expectedHashes[i] = calculateExpectedMerkleRoot(content)
	}

	return files, expectedHashes
}

// calculateExpectedMerkleRoot calculates the expected merkle root for content
func calculateExpectedMerkleRoot(content []byte) [32]byte {
	hasher := merkle.NewHash()
	hasher.Write(content)
	var root [32]byte
	hasher.Sum(root[:0])
	return root
}

// verifyFileHashes verifies that the hashed results match expected values
func verifyFileHashes(t *testing.T, results []fileHash, expected [][32]byte) {
	t.Helper()

	if len(results) != len(expected) {
		t.Fatalf("result count mismatch: got %d, want %d", len(results), len(expected))
	}

	for i, result := range results {
		if !bytes.Equal(result.piecesRoot[:], expected[i][:]) {
			t.Errorf("file %d: piecesRoot mismatch", i)
		}
	}
}

// TestFileHasher_LargeFileWithPieceLayers tests that piece layers are correctly extracted
func TestFileHasher_LargeFileWithPieceLayers(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping large file test in short mode")
	}

	// Create 256 KiB file with 64 KiB piece length = 4 pieces
	fileSize := int64(256 * 1024)
	pieceLength := int64(64 * 1024)

	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "large.bin")

	content := make([]byte, fileSize)
	for i := range content {
		content[i] = byte(i % 256)
	}

	if err := os.WriteFile(testFile, content, 0644); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	files := []fileEntry{{
		path:   testFile,
		length: fileSize,
	}}

	hasher := NewFileHasher(files, pieceLength, &mockDisplay{}, 1, false)
	if err := hasher.hashFiles(); err != nil {
		t.Fatalf("hashFiles failed: %v", err)
	}

	if len(hasher.results) != 1 {
		t.Fatal("expected 1 result")
	}

	result := hasher.results[0]

	// Check that piece layers exist (file > piece length)
	if result.pieceLayers == "" {
		t.Error("expected pieceLayers for file larger than piece length")
	}

	// Should have 4 piece layer hashes (4 pieces * 32 bytes)
	expectedLayersLen := 4 * 32
	if len(result.pieceLayers) != expectedLayersLen {
		t.Errorf("pieceLayers length = %d, want %d", len(result.pieceLayers), expectedLayersLen)
	}
}

// TestFileHasher_EmptyFile tests hashing of empty file
func TestFileHasher_EmptyFile(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "empty.txt")

	if err := os.WriteFile(testFile, []byte{}, 0644); err != nil {
		t.Fatalf("Failed to create empty file: %v", err)
	}

	files := []fileEntry{{
		path:   testFile,
		length: 0,
	}}

	hasher := NewFileHasher(files, 1<<16, &mockDisplay{}, 1, false)
	if err := hasher.hashFiles(); err != nil {
		t.Fatalf("hashFiles failed: %v", err)
	}

	if len(hasher.results) != 1 {
		t.Fatal("expected 1 result")
	}

	result := hasher.results[0]

	// Empty file should have all-zero piecesRoot
	var expectedRoot [32]byte
	if result.piecesRoot != expectedRoot {
		t.Error("empty file should have all-zero piecesRoot")
	}

	if result.pieceLayers != "" {
		t.Error("empty file should not have pieceLayers")
	}
}

// TestFileHasher_SmallFileNoPieceLayers tests that small files don't have piece layers
func TestFileHasher_SmallFileNoPieceLayers(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "small.txt")

	content := []byte("small file content")
	if err := os.WriteFile(testFile, content, 0644); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	files := []fileEntry{{
		path:   testFile,
		length: int64(len(content)),
	}}

	pieceLength := int64(1 << 16) // 64KB
	hasher := NewFileHasher(files, pieceLength, &mockDisplay{}, 1, false)
	if err := hasher.hashFiles(); err != nil {
		t.Fatalf("hashFiles failed: %v", err)
	}

	result := hasher.results[0]

	// Small file should not have piece layers
	if result.pieceLayers != "" {
		t.Error("small file should not have pieceLayers")
	}

	// But should have a valid piecesRoot
	var zeroRoot [32]byte
	if result.piecesRoot == zeroRoot {
		t.Error("small file should have non-zero piecesRoot")
	}
}

// TestFileHasher_OptimizeWorkers tests the worker optimization logic
func TestFileHasher_OptimizeWorkers(t *testing.T) {
	tests := []struct {
		name        string
		numFiles    int
		fileSize    int64
		minExpected int
		maxExpected int
	}{
		{
			name:        "single tiny file",
			numFiles:    1,
			fileSize:    1 << 10, // 1KB
			minExpected: 1,
			maxExpected: 1,
		},
		{
			name:        "single large file",
			numFiles:    1,
			fileSize:    1 << 30, // 1GB
			minExpected: 1,
			maxExpected: runtime.NumCPU(),
		},
		{
			name:        "many small files",
			numFiles:    100,
			fileSize:    1 << 20, // 1MB each
			minExpected: 1,
			maxExpected: runtime.NumCPU(),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			files := make([]fileEntry, tt.numFiles)

			for i := 0; i < tt.numFiles; i++ {
				path := filepath.Join(tmpDir, fmt.Sprintf("file_%d.dat", i))
				content := make([]byte, tt.fileSize)
				if err := os.WriteFile(path, content, 0644); err != nil {
					t.Fatalf("Failed to create test file: %v", err)
				}
				files[i] = fileEntry{
					path:   path,
					length: tt.fileSize,
				}
			}

			hasher := NewFileHasher(files, 1<<16, &mockDisplay{}, 0, false)
			workers := hasher.optimizeWorkers()

			if workers < tt.minExpected || workers > tt.maxExpected {
				t.Errorf("optimizeWorkers() = %d, want between %d and %d",
					workers, tt.minExpected, tt.maxExpected)
			}
		})
	}
}

// TestFileHasher_HashConsistency tests that hashing produces consistent results
func TestFileHasher_HashConsistency(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "test.txt")

	content := []byte("test content for consistency check")
	if err := os.WriteFile(testFile, content, 0644); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	files := []fileEntry{{
		path:   testFile,
		length: int64(len(content)),
	}}

	pieceLength := int64(1 << 16)

	// Hash multiple times and verify same result
	var firstResult [32]byte
	for i := 0; i < 3; i++ {
		hasher := NewFileHasher(files, pieceLength, &mockDisplay{}, 1, false)
		if err := hasher.hashFiles(); err != nil {
			t.Fatalf("hashFiles failed: %v", err)
		}

		if i == 0 {
			firstResult = hasher.results[0].piecesRoot
		} else {
			if hasher.results[0].piecesRoot != firstResult {
				t.Errorf("hash inconsistency at iteration %d", i)
			}
		}
	}
}
