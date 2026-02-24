package torrent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCreateTorrent_FormatRouting verifies that CreateTorrent correctly routes
// to the appropriate format implementation based on the Format option.
// It tests v1 (default/explicit), v2, hybrid, and invalid format values.
func TestCreateTorrent_FormatRouting(t *testing.T) {
	// Create a temporary test file
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "test.txt")
	if err := os.WriteFile(testFile, []byte("test content for torrent creation"), 0644); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	tests := []struct {
		name        string
		format      int
		wantErr     bool
		errContains string
	}{
		{
			name:    "v1 format (default/zero)",
			format:  0,
			wantErr: false,
		},
		{
			name:    "v1 format (explicit)",
			format:  FormatV1,
			wantErr: false,
		},
		{
			name:    "v2 format (implemented)",
			format:  FormatV2,
			wantErr: false,
		},
		{
			name:        "hybrid format (not implemented)",
			format:      FormatHybrid,
			wantErr:     true,
			errContains: "not yet implemented",
		},
		{
			name:        "invalid format",
			format:      99,
			wantErr:     true,
			errContains: "invalid format version",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := CreateOptions{
				Path:      testFile,
				Format:    tt.format,
				NoDate:    true,
				NoCreator: true,
			}

			torrent, err := CreateTorrent(opts)

			if tt.wantErr {
				if err == nil {
					t.Errorf("CreateTorrent() error = nil, wantErr %v", tt.wantErr)
					return
				}
				if tt.errContains != "" && !strings.Contains(err.Error(), tt.errContains) {
					t.Errorf("CreateTorrent() error = %v, should contain %q", err, tt.errContains)
				}
				return
			}

			if err != nil {
				t.Errorf("CreateTorrent() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if torrent == nil {
				t.Error("CreateTorrent() returned nil torrent without error")
				return
			}

			// Verify the torrent was created successfully
			// The torrent is valid if it has InfoBytes (bencoded info dictionary)
			if torrent.MetaInfo != nil && len(torrent.MetaInfo.InfoBytes) == 0 {
				t.Error("v1 torrent should have InfoBytes")
			}
		})
	}
}
