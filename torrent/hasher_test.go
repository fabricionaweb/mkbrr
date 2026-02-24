package torrent

import (
	"bytes"
	"crypto/sha1"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"

	"github.com/anacrolix/torrent/merkle"
	"github.com/autobrr/mkbrr/internal/trackers"
)

// mockDisplay implements Displayer interface for testing
type mockDisplay struct{}

func (m *mockDisplay) ShowProgress(total int)                      {}
func (m *mockDisplay) UpdateProgress(count int, hashrate float64)  {}
func (m *mockDisplay) ShowFiles(files []fileEntry, numWorkers int) {}
func (m *mockDisplay) ShowSeasonPackWarnings(info *SeasonPackInfo) {}
func (m *mockDisplay) FinishProgress()                             {}
func (m *mockDisplay) IsBatch() bool                               { return true }

// TestPieceHasher_Concurrent tests the hasher with various real-world scenarios.
// Test cases are designed to cover common torrent types and sizes:
//   - Piece sizes: 64KB for small files (standard minimum)
//     4MB for large files
//   - Worker counts: 1 (sequential baseline)
//     2 (minimal concurrency)
//     4 (typical desktop CPU)
//     8 (high-end desktop/server)
func TestPieceHasher_Concurrent(t *testing.T) {
	tests := []struct {
		name      string
		numFiles  int
		fileSize  int64
		pieceLen  int64
		numPieces int
	}{
		{
			name:      "single small file",
			numFiles:  1,
			fileSize:  1 << 20, // 1MB
			pieceLen:  1 << 16, // 64KB
			numPieces: 16,
		},
		{
			name:      "1080p episode",
			numFiles:  1,
			fileSize:  4 << 30, // 4GB
			pieceLen:  1 << 22, // 4MB
			numPieces: 1024,
		},
		{
			name:      "multi-file album",
			numFiles:  12,       // typical album length
			fileSize:  40 << 20, // 40MB per track
			pieceLen:  1 << 16,  // 64KB pieces
			numPieces: 7680,     // ~480MB total
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			totalSize := tt.fileSize * int64(tt.numFiles)
			if totalSize > 1<<30 {
				// skip large tests on Windows to avoid CI slowdowns
				if runtime.GOOS == "windows" {
					t.Skipf("skipping large file test %s on Windows", tt.name)
				}
				// also skip in short mode if total size > 1GB
				if testing.Short() {
					t.Skipf("skipping large file test %s in short mode", tt.name)
				}
			}

			files, expectedHashes := createTestFilesFast(t, tt.numFiles, tt.fileSize, tt.pieceLen)
			hasher := NewPieceHasher(files, tt.pieceLen, tt.numPieces, &mockDisplay{}, false)

			// test with different worker counts
			workerCounts := []int{1, 2, 4, 8}
			for _, workers := range workerCounts {
				t.Run(fmt.Sprintf("workers_%d", workers), func(t *testing.T) {
					if err := hasher.hashPieces(workers); err != nil {
						t.Fatalf("hashPieces failed with %d workers: %v", workers, err)
					}
					verifyHashes(t, hasher.pieces, expectedHashes)
				})
			}
		})
	}
}

// createTestFilesFast creates test files more efficiently using sparse files
func createTestFilesFast(t *testing.T, numFiles int, fileSize, pieceLen int64) ([]fileEntry, [][]byte) {
	t.Helper()

	tempDir, err := os.MkdirTemp("", "hasher_test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(tempDir) })

	var files []fileEntry
	var expectedHashes [][]byte
	var offset int64

	// create a repeatable pattern that's more representative of real data
	pattern := make([]byte, pieceLen)
	for i := range pattern {
		// create a pseudo-random but deterministic pattern
		pattern[i] = byte((i*7 + 13) % 251) // prime numbers help create distribution
	}

	for i := 0; i < numFiles; i++ {
		path := filepath.Join(tempDir, fmt.Sprintf("test_file_%d", i))

		f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0644)
		if err != nil {
			t.Fatalf("failed to create file: %v", err)
		}

		// write pattern at start and end of file to simulate real data
		// while keeping the file sparse in the middle
		if _, err := f.Write(pattern); err != nil {
			f.Close()
			t.Fatalf("failed to write pattern: %v", err)
		}

		if _, err := f.Seek(fileSize-int64(len(pattern)), io.SeekStart); err != nil {
			f.Close()
			t.Fatalf("failed to seek: %v", err)
		}

		if _, err := f.Write(pattern); err != nil {
			f.Close()
			t.Fatalf("failed to write pattern: %v", err)
		}

		if err := f.Truncate(fileSize); err != nil {
			f.Close()
			t.Fatalf("failed to truncate file: %v", err)
		}
		f.Close()

		files = append(files, fileEntry{
			path:   path,
			length: fileSize,
			offset: offset,
		})
		offset += fileSize

		// calculate expected hashes with the pattern
		h := sha1.New()
		for pos := int64(0); pos < fileSize; pos += pieceLen {
			h.Reset()
			if pos == 0 || pos >= fileSize-pieceLen {
				h.Write(pattern) // use pattern for first and last pieces
			} else {
				h.Write(make([]byte, pieceLen)) // zero bytes for middle pieces
			}
			expectedHashes = append(expectedHashes, h.Sum(nil))
		}
	}

	return files, expectedHashes
}

// createTestFilesWithPattern creates test files filled with a deterministic pattern.
func createTestFilesWithPattern(t *testing.T, tempDir string, fileSizes []int64, pieceLen int64) ([]fileEntry, [][]byte) {
	t.Helper()

	var files []fileEntry
	var allExpectedHashes [][]byte
	var offset int64
	var globalBuffer bytes.Buffer

	// create a repeatable pattern that's more representative of real data
	pattern := make([]byte, pieceLen)
	for i := range pattern {
		pattern[i] = byte((i*11 + 17) % 253) // different primes for variety
	}

	for i, fileSize := range fileSizes {
		path := filepath.Join(tempDir, fmt.Sprintf("boundary_test_file_%d", i))
		f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0644)
		if err != nil {
			t.Fatalf("failed to create file %s: %v", path, err)
		}

		// write the pattern repeatedly to fill the file
		written := int64(0)
		for written < fileSize {
			toWrite := pieceLen
			if written+toWrite > fileSize {
				toWrite = fileSize - written
			}
			n, err := f.Write(pattern[:toWrite])
			if err != nil {
				f.Close()
				t.Fatalf("failed to write pattern to %s: %v", path, err)
			}
			// also write to global buffer for hash calculation
			globalBuffer.Write(pattern[:toWrite])
			written += int64(n)
		}
		f.Close()

		files = append(files, fileEntry{
			path:   path,
			length: fileSize,
			offset: offset,
		})
		offset += fileSize
	}

	// calculate expected hashes from the global buffer
	globalData := globalBuffer.Bytes()
	totalSize := int64(len(globalData))
	numPieces := (totalSize + pieceLen - 1) / pieceLen

	h := sha1.New()
	for i := int64(0); i < numPieces; i++ {
		start := i * pieceLen
		end := start + pieceLen
		if end > totalSize {
			end = totalSize
		}
		h.Reset()
		h.Write(globalData[start:end])
		allExpectedHashes = append(allExpectedHashes, h.Sum(nil))
	}

	return files, allExpectedHashes
}

func verifyHashes(t *testing.T, got, want [][]byte) {
	t.Helper()

	if len(got) != len(want) {
		t.Fatalf("piece count mismatch: got %d, want %d", len(got), len(want))
	}

	for i := range got {
		if !bytes.Equal(got[i], want[i]) {
			t.Errorf("piece %d hash mismatch:\ngot  %x\nwant %x", i, got[i], want[i])
		}
	}
}

// TestPieceHasher_EdgeCases tests various edge cases and error conditions
func TestPieceHasher_EdgeCases(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "hasher_test_edge")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	tests := []struct {
		name       string
		setup      func() []fileEntry
		pieceLen   int64
		numPieces  int
		wantErr    bool
		skipOnWin  bool
		skipOnRoot bool
	}{
		{
			name: "non-existent file",
			setup: func() []fileEntry {
				return []fileEntry{{
					path:   filepath.Join(tempDir, "nonexistent"),
					length: 1024,
					offset: 0,
				}}
			},
			pieceLen:  64,
			numPieces: 16,
			wantErr:   true,
		},
		{
			name: "empty file",
			setup: func() []fileEntry {
				path := filepath.Join(tempDir, "empty")
				if err := os.WriteFile(path, []byte{}, 0644); err != nil {
					t.Fatalf("failed to create empty file: %v", err)
				}
				return []fileEntry{{
					path:   path,
					length: 0,
					offset: 0,
				}}
			},
			pieceLen:  64,
			numPieces: 1,
			wantErr:   false,
		},
		{
			name: "unreadable file",
			setup: func() []fileEntry {
				path := filepath.Join(tempDir, "unreadable")
				if err := os.WriteFile(path, []byte("test"), 0000); err != nil {
					t.Fatalf("failed to create unreadable file: %v", err)
				}
				return []fileEntry{{
					path:   path,
					length: 4,
					offset: 0,
				}}
			},
			pieceLen:   64,
			numPieces:  1,
			wantErr:    true,
			skipOnWin:  true,
			skipOnRoot: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.skipOnWin && runtime.GOOS == "windows" {
				t.Skip("skipping unreadable file test on Windows")
			}
			if tt.skipOnRoot && os.Geteuid() == 0 {
				t.Skip("skipping unreadable file test when running as root")
			}
			files := tt.setup()
			hasher := NewPieceHasher(files, tt.pieceLen, tt.numPieces, &mockDisplay{}, false)

			err := hasher.hashPieces(2)
			if (err != nil) != tt.wantErr {
				t.Errorf("hashPieces() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

// TestPieceHasher_RaceConditions tests concurrent access using a FLAC album scenario.
// FLAC albums are ideal for race testing because:
// - multiple small-to-medium files (12 tracks, 40MB each)
// - small piece size (64KB) creates more concurrent operations
func TestPieceHasher_RaceConditions(t *testing.T) {
	// use the multi-file album test case from TestPieceHasher_Concurrent
	// but run multiple hashers concurrently to stress test race conditions
	numFiles := 12
	fileSize := int64(40 << 20) // 40MB per track
	pieceLen := int64(1 << 16)  // 64KB pieces
	numPieces := 7680           // ~480MB total

	tempDir, err := os.MkdirTemp("", "hasher_race_test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	files, expectedHashes := createTestFilesFast(t, numFiles, fileSize, pieceLen)

	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			hasher := NewPieceHasher(files, pieceLen, numPieces, &mockDisplay{}, false)
			if err := hasher.hashPieces(4); err != nil {
				t.Errorf("hashPieces failed: %v", err)
				return
			}
			verifyHashes(t, hasher.pieces, expectedHashes)
		}()
	}
	wg.Wait()
}

func TestPieceHasher_NoFiles(t *testing.T) {
	hasher := NewPieceHasher([]fileEntry{}, 1<<16, 0, &mockDisplay{}, false)

	err := hasher.hashPieces(0)
	if err != nil {
		t.Errorf("hashPieces() with no files should not return an error, got %v", err)
	}

	if len(hasher.pieces) != 0 {
		t.Errorf("expected 0 pieces, got %d", len(hasher.pieces))
	}
}

func TestPieceHasher_ZeroWorkers(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "hasher_zero_workers")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	files := []fileEntry{{
		path:   filepath.Join(tempDir, "test"),
		length: 1 << 16,
		offset: 0,
	}}

	// Create the actual test file
	filePath := files[0].path
	fileSize := files[0].length
	f, err := os.Create(filePath)
	if err != nil {
		t.Fatalf("failed to create test file %s: %v", filePath, err)
	}
	if err := f.Truncate(fileSize); err != nil {
		f.Close()
		t.Fatalf("failed to truncate test file %s: %v", filePath, err)
	}
	f.Close()

	hasher := NewPieceHasher(files, 1<<16, 1, &mockDisplay{}, false)

	// Calling with 0 workers should now trigger automatic optimization or default to 1 worker,
	// so it should NOT return an error in this case.
	err = hasher.hashPieces(0)
	if err != nil {
		t.Errorf("hashPieces(0) should not return an error (should optimize or default to 1 worker), but got: %v", err)
	}
}

func TestPieceHasher_CorruptedData(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "hasher_corrupt_test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	files, expectedHashes := createTestFilesFast(t, 1, 1<<16, 1<<16) // 1 file, 64KB

	// corrupt the file by modifying a byte
	corruptedPath := files[0].path
	data, err := os.ReadFile(corruptedPath)
	if err != nil {
		t.Fatalf("failed to read file: %v", err)
	}
	data[0] ^= 0xFF // flip bits of first byte
	if err := os.WriteFile(corruptedPath, data, 0644); err != nil {
		t.Fatalf("failed to write corrupted file: %v", err)
	}

	hasher := NewPieceHasher(files, 1<<16, 1, &mockDisplay{}, false)
	if err := hasher.hashPieces(1); err != nil {
		t.Fatalf("hashPieces failed: %v", err)
	}

	if bytes.Equal(hasher.pieces[0], expectedHashes[0]) {
		t.Errorf("expected hash mismatch due to corrupted data, but hashes matched")
	}
}

// TestPieceHasher_BoundaryConditions tests scenarios where file boundaries
// align exactly with piece boundaries.
func TestPieceHasher_BoundaryConditions(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "hasher_boundary_test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	pieceLen := int64(1 << 16) // 64KB

	tests := []struct {
		name      string
		fileSizes []int64 // Sizes of consecutive files
	}{
		{
			name:      "Exact Piece Boundary",
			fileSizes: []int64{pieceLen, pieceLen * 2, pieceLen}, // Files end exactly on piece boundaries
		},
		{
			name:      "Mid-Piece Boundary",
			fileSizes: []int64{pieceLen / 2, pieceLen, pieceLen * 2, pieceLen / 3}, // Boundaries within pieces
		},
		{
			name:      "Multiple Small Files within One Piece",
			fileSizes: []int64{pieceLen / 4, pieceLen / 4, pieceLen / 4, pieceLen / 4},
		},
		{
			name:      "Large File Followed By Small",
			fileSizes: []int64{pieceLen * 3, pieceLen / 2},
		},
		{
			name:      "Small File Followed By Large",
			fileSizes: []int64{pieceLen / 2, pieceLen * 3},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			files, expectedHashes := createTestFilesWithPattern(t, tempDir, tt.fileSizes, pieceLen)

			var totalSize int64
			for _, size := range tt.fileSizes {
				totalSize += size
			}
			numPieces := (totalSize + pieceLen - 1) / pieceLen

			// Test with 1 and multiple workers
			workerCounts := []int{1, 4}
			for _, workers := range workerCounts {
				t.Run(fmt.Sprintf("workers_%d", workers), func(t *testing.T) {
					// Need to create a new hasher instance for each run if pieces are modified in place
					currentHasher := NewPieceHasher(files, pieceLen, int(numPieces), &mockDisplay{}, false)
					if err := currentHasher.hashPieces(workers); err != nil {
						t.Fatalf("hashPieces failed with %d workers: %v", workers, err)
					}
					verifyHashes(t, currentHasher.pieces, expectedHashes)
				})
			}
			// Clean up files for this subtest run
			for _, f := range files {
				os.Remove(f.path)
			}
		})
	}
}

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

// TestTorrentFileSize verifies that created torrent files respect tracker size limits
func TestTorrentFileSize(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "torrent_size_test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	tests := []struct {
		name       string
		trackerURL string
		fileSize   int64
		numFiles   int
		pieceLen   uint
		wantError  bool
	}{
		{
			name:       "small torrent for ant should be under 250 KiB",
			trackerURL: "https://anthelion.me/announce",
			fileSize:   100 << 20, // 100 MB (down from 1 GB)
			numFiles:   1,
			pieceLen:   22, // 4 MiB pieces to keep torrent small
			wantError:  false,
		},
		{
			name:       "large torrent for ant should adjust piece length",
			trackerURL: "https://anthelion.me/announce",
			fileSize:   1 << 30, // 1 GB (down from 10 GB)
			numFiles:   20,      // 20 files (down from 100)
			pieceLen:   16,      // start with 64 KiB pieces
			wantError:  false,   // should succeed by adjusting piece length
		},
		{
			name:       "small torrent for ggn should be under 1 MB",
			trackerURL: "https://gazellegames.net/announce",
			fileSize:   100 << 20, // 100 MB (down from 1 GB)
			numFiles:   1,
			pieceLen:   22, // 4 MiB pieces to keep torrent small
			wantError:  false,
		},
		{
			name:       "large torrent for ggn should adjust piece length",
			trackerURL: "https://gazellegames.net/announce",
			fileSize:   5 << 30, // 5 GB (down from 50 GB)
			numFiles:   50,      // 50 files (down from 500)
			pieceLen:   16,      // start with 64 KiB pieces
			wantError:  false,   // should succeed by adjusting piece length
		},
		{
			name:       "large torrent for ptp should be fine",
			trackerURL: "https://passthepopcorn.me/announce",
			fileSize:   1 << 30, // 1 GB (down from 10 GB)
			numFiles:   20,      // 20 files (down from 100)
			pieceLen:   16,      // even with small pieces, PTP has no limit
			wantError:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create test files
			testPath := filepath.Join(tempDir, tt.name)
			if err := os.MkdirAll(testPath, 0755); err != nil {
				t.Fatalf("failed to create test dir: %v", err)
			}

			// Create the test files (sparse)
			for i := 0; i < tt.numFiles; i++ {
				filePath := filepath.Join(testPath, fmt.Sprintf("file_%d", i))
				f, err := os.Create(filePath)
				if err != nil {
					t.Fatalf("failed to create file: %v", err)
				}
				if err := f.Truncate(tt.fileSize / int64(tt.numFiles)); err != nil {
					f.Close()
					t.Fatalf("failed to truncate file: %v", err)
				}
				f.Close()
			}

			// Create torrent
			opts := CreateOptions{
				Path:           testPath,
				TrackerURLs:    []string{tt.trackerURL},
				PieceLengthExp: &tt.pieceLen,
				IsPrivate:      true,
				Verbose:        true,
			}

			torrentPath := filepath.Join(tempDir, tt.name+".torrent")
			opts.OutputPath = torrentPath

			_, err := Create(opts)
			if tt.wantError {
				if err == nil {
					// Check actual file size if creation succeeded
					info, err := os.Stat(torrentPath)
					if err != nil {
						t.Fatalf("failed to stat torrent file: %v", err)
					}
					t.Errorf("expected error for large torrent, got success. Torrent size: %d bytes", info.Size())
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				} else {
					// Verify the torrent file size is under the tracker's limit
					info, err := os.Stat(torrentPath)
					if err != nil {
						t.Fatalf("failed to stat torrent file: %v", err)
					}

					if maxSize, ok := trackers.GetTrackerMaxTorrentSize(tt.trackerURL); ok {
						if uint64(info.Size()) > maxSize {
							t.Errorf("torrent file size %d exceeds tracker limit %d", info.Size(), maxSize)
						} else {
							t.Logf("successfully created torrent under size limit: %d bytes (limit: %d)", info.Size(), maxSize)
						}
					}
				}
			}
		})
	}
}
