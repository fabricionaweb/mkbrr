package torrent

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestProcessBatch(t *testing.T) {
	// create a temporary directory for test files
	tmpDir, err := os.MkdirTemp("", "mkbrr-batch-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// create test files and directories
	testFiles := []struct {
		path    string
		content string
	}{
		{
			path:    "file1.txt",
			content: "test file 1 content",
		},
		{
			path:    "dir1/file2.txt",
			content: "test file 2 content",
		},
		{
			path:    "dir1/file3.txt",
			content: "test file 3 content",
		},
	}

	for _, tf := range testFiles {
		path := filepath.Join(tmpDir, tf.path)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatalf("Failed to create directory: %v", err)
		}
		if err := os.WriteFile(path, []byte(tf.content), 0644); err != nil {
			t.Fatalf("Failed to write test file: %v", err)
		}
	}

	// create batch config file
	configPath := filepath.Join(tmpDir, "batch.yaml")
	configContent := []byte(fmt.Sprintf(`version: 1
jobs:
  - output: %s
    path: %s
    name: "Test File 1"
    trackers:
      - udp://tracker.example.com:1337/announce
    private: true
    piece_length: 16
  - output: %s
    path: %s
    name: "Test Directory"
    trackers:
      - udp://tracker.example.com:1337/announce
    webseeds:
      - https://example.com/files/
    comment: "Test batch torrent"
`,
		filepath.Join(tmpDir, "file1.torrent"),
		filepath.Join(tmpDir, "file1.txt"),
		filepath.Join(tmpDir, "dir1.torrent"),
		filepath.Join(tmpDir, "dir1")))

	if err := os.WriteFile(configPath, configContent, 0644); err != nil {
		t.Fatalf("Failed to write config file: %v", err)
	}

	// process batch
	results, err := ProcessBatch(configPath, true, false, false, "test-version")
	if err != nil {
		t.Fatalf("ProcessBatch failed: %v", err)
	}

	// verify results
	if len(results) != 2 {
		t.Errorf("Expected 2 results, got %d", len(results))
	}

	for i, result := range results {
		if !result.Success {
			t.Errorf("Job %d failed: %v", i, result.Error)
			continue
		}

		if result.Info == nil {
			t.Errorf("Job %d missing info", i)
			continue
		}

		// verify torrent files were created
		if _, err := os.Stat(result.Info.Path); err != nil {
			t.Errorf("Job %d torrent file not created: %v", i, err)
		}

		// basic validation of torrent info
		if result.Info.InfoHash == "" {
			t.Errorf("Job %d missing info hash", i)
		}

		if result.Info.Size == 0 {
			t.Errorf("Job %d has zero size", i)
		}

		// check specific job details
		switch i {
		case 0: // file1.txt
			if result.Info.Files != 0 {
				t.Errorf("Expected single file torrent, got %d files", result.Info.Files)
			}
		case 1: // dir1
			if result.Info.Files != 2 {
				t.Errorf("Expected 2 files in directory torrent, got %d", result.Info.Files)
			}
		}
	}
}

func TestProcessBatchV2(t *testing.T) {
	// create a temporary directory for test files
	tmpDir, err := os.MkdirTemp("", "mkbrr-batch-v2-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// create test files
	testFiles := []struct {
		path    string
		content string
	}{
		{path: "file1.txt", content: "test file 1 content"},
		{path: "dir1/file2.txt", content: "test file 2 content"},
	}

	for _, tf := range testFiles {
		path := filepath.Join(tmpDir, tf.path)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatalf("Failed to create directory: %v", err)
		}
		if err := os.WriteFile(path, []byte(tf.content), 0644); err != nil {
			t.Fatalf("Failed to write test file: %v", err)
		}
	}

	// create batch config file with v2 format
	configPath := filepath.Join(tmpDir, "batch.yaml")
	configContent := []byte(fmt.Sprintf(`version: 1
jobs:
  - output: %s
    path: %s
    name: "V2 Single File"
    format: v2
    trackers:
      - udp://tracker.example.com:1337/announce
    piece_length: 14
  - output: %s
    path: %s
    name: "V2 Directory"
    format: "2"
    trackers:
      - udp://tracker.example.com:1337/announce
    piece_length: 15
`,
		filepath.Join(tmpDir, "v2-single.torrent"),
		filepath.Join(tmpDir, "file1.txt"),
		filepath.Join(tmpDir, "v2-dir.torrent"),
		filepath.Join(tmpDir, "dir1")))

	if err := os.WriteFile(configPath, configContent, 0644); err != nil {
		t.Fatalf("Failed to write config file: %v", err)
	}

	// process batch
	results, err := ProcessBatch(configPath, true, false, false, "test-version")
	if err != nil {
		t.Fatalf("ProcessBatch failed: %v", err)
	}

	// verify results
	if len(results) != 2 {
		t.Errorf("Expected 2 results, got %d", len(results))
	}

	for i, result := range results {
		if !result.Success {
			t.Errorf("Job %d failed: %v", i, result.Error)
			continue
		}

		if result.Info == nil {
			t.Errorf("Job %d missing info", i)
			continue
		}

		// verify torrent files were created
		if _, err := os.Stat(result.Info.Path); err != nil {
			t.Errorf("Job %d torrent file not created: %v", i, err)
		}

		// v2 torrents should have SHA-256 hash (64 hex chars)
		if len(result.Info.InfoHash) != 64 {
			t.Errorf("Job %d expected v2 SHA-256 hash (64 chars), got %d chars: %s", i, len(result.Info.InfoHash), result.Info.InfoHash)
		}

		// check specific job details
		switch i {
		case 0: // v2 single file with piece_length 14 (allowed in v2)
			if result.Info.Files != 0 {
				t.Errorf("Expected single file torrent, got %d files", result.Info.Files)
			}
		case 1: // v2 directory with piece_length 15
			// v2 uses FileTree, so Files will be 0. Check total size instead.
			if result.Info.Size == 0 {
				t.Errorf("Expected non-zero size in directory torrent, got 0")
			}
		}
	}
}

func TestBatchValidation(t *testing.T) {
	tests := []struct {
		name        string
		config      string
		expectError bool
	}{
		{
			name: "invalid version",
			config: `version: 2
jobs:
  - output: test.torrent
    path: %s`,
			expectError: true,
		},
		{
			name: "missing path",
			config: `version: 1
jobs:
  - output: test.torrent`,
			expectError: true,
		},
		{
			name: "missing output",
			config: `version: 1
jobs:
  - path: %s`,
			expectError: true,
		},
		{
			name: "invalid piece length too large",
			config: `version: 1
jobs:
  - output: test.torrent
    path: %s
    piece_length: 28`,
			expectError: true,
		},
		{
			name: "v1 invalid piece length 14 (min is 16)",
			config: `version: 1
jobs:
  - output: test.torrent
    path: %s
    format: v1
    piece_length: 14`,
			expectError: true,
		},
		{
			name: "v1 invalid piece length 15 (min is 16)",
			config: `version: 1
jobs:
  - output: test.torrent
    path: %s
    format: "1"
    piece_length: 15`,
			expectError: true,
		},
		{
			name: "v2 valid piece length 14",
			config: `version: 1
jobs:
  - output: %s.torrent
    path: %s
    format: v2
    piece_length: 14`,
			expectError: false,
		},
		{
			name: "v2 valid piece length 15",
			config: `version: 1
jobs:
  - output: %s.torrent
    path: %s
    format: "2"
    piece_length: 15`,
			expectError: false,
		},
		{
			name: "empty jobs",
			config: `version: 1
jobs: []`,
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir, err := os.MkdirTemp("", "mkbrr-batch-validation")
			if err != nil {
				t.Fatalf("Failed to create temp dir: %v", err)
			}
			defer os.RemoveAll(tmpDir)

			// Create test file for validation (path must exist)
			testFile := filepath.Join(tmpDir, "test.txt")
			if err := os.WriteFile(testFile, []byte("test"), 0644); err != nil {
				t.Fatalf("Failed to create test file: %v", err)
			}

			// Update config to use absolute path
			// Some tests have two %s placeholders (output and path), so pass testFile twice
			configContent := fmt.Sprintf(tt.config, testFile, testFile)

			configPath := filepath.Join(tmpDir, "batch.yaml")
			if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
				t.Fatalf("Failed to write config file: %v", err)
			}

			_, err = ProcessBatch(configPath, false, false, false, "test-version")
			if tt.expectError && err == nil {
				t.Error("Expected error but got nil")
			}
			if !tt.expectError && err != nil {
				t.Errorf("Unexpected error: %v", err)
			}
		})
	}
}
