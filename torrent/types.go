package torrent

import (
	"fmt"
	"os"
	"strings"

	"github.com/anacrolix/torrent/metainfo"
)

// FormatVersion represents the BitTorrent format version
const (
	FormatV1     = 1 // v1 only (default)
	FormatV2     = 2 // v2 only
	FormatHybrid = 3 // v1 + v2 hybrid
)

// ParseFormat parses a format string into a FormatVersion constant.
// Accepts: "1"/"v1", "2"/"v2", "3"/"hybrid"
func ParseFormat(s string) (int, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "v1":
		return FormatV1, nil
	case "2", "v2":
		return FormatV2, nil
	case "3", "hybrid":
		return FormatHybrid, nil
	default:
		return 0, fmt.Errorf("invalid format: %q (expected: 1/v1, 2/v2, or 3/hybrid)", s)
	}
}

// ProgressCallback is called during torrent creation to report progress.
// completed: number of pieces hashed so far
// total: total number of pieces to hash
// hashRate: current hashing rate in bytes per second
type ProgressCallback func(completed, total int, hashRate float64)

// CreateOptions contains all options for creating a torrent
type CreateOptions struct {
	PieceLengthExp          *uint
	MaxPieceLength          *uint
	Path                    string
	Name                    string
	TrackerURLs             []string
	Comment                 string
	Source                  string
	Version                 string
	OutputPath              string
	OutputDir               string
	WebSeeds                []string
	ExcludePatterns         []string
	IncludePatterns         []string
	Workers                 int
	IsPrivate               bool
	NoDate                  bool
	NoCreator               bool
	Verbose                 bool
	Entropy                 bool
	Quiet                   bool
	InfoOnly                bool
	SkipPrefix              bool
	FailOnSeasonPackWarning bool
	// Format specifies the BitTorrent format version.
	// 0 or 1 = v1 (default), 2 = v2 only, 3 = hybrid (v1+v2)
	Format                  int
	// ProgressCallback is called during hashing to report progress.
	// If nil, no progress callbacks will be made.
	ProgressCallback        ProgressCallback
}

// Torrent represents a torrent file with additional functionality
type Torrent struct {
	*metainfo.MetaInfo
}

// FileEntry represents a file in the torrent
type FileEntry struct {
	Name string
	Path string
	Size int64
}

// internal file entry for processing
type fileEntry struct {
	path   string
	length int64
	offset int64
}

// internal file reader for processing
type fileReader struct {
	file     *os.File
	position int64
	length   int64
}

// TorrentInfo contains summary information about the created torrent
type TorrentInfo struct {
	MetaInfo *metainfo.MetaInfo
	Path     string
	InfoHash string
	Announce string
	Size     int64
	Files    int
}

// VerificationResult holds the outcome of a torrent data verification check
type VerificationResult struct {
	BadPieceIndices []int
	MissingFiles    []string
	TotalPieces     int
	GoodPieces      int
	BadPieces       int
	MissingPieces   int
	Completion      float64
}

// callbackDisplayer adapts a ProgressCallback to the Displayer interface
type callbackDisplayer struct {
	callback ProgressCallback
	total    int
}

// ShowProgress implements Displayer interface
func (c *callbackDisplayer) ShowProgress(total int) {
	c.total = total
	if c.callback != nil {
		c.callback(0, total, 0)
	}
}

// UpdateProgress implements Displayer interface
func (c *callbackDisplayer) UpdateProgress(completed int, hashrate float64) {
	if c.callback != nil {
		c.callback(completed, c.total, hashrate)
	}
}

// ShowFiles implements Displayer interface (no-op for callback)
func (c *callbackDisplayer) ShowFiles(files []fileEntry, numWorkers, blockWorkers int) {}

// ShowSeasonPackWarnings implements Displayer interface (no-op for callback)
func (c *callbackDisplayer) ShowSeasonPackWarnings(info *SeasonPackInfo) {}

// FinishProgress implements Displayer interface
func (c *callbackDisplayer) FinishProgress() {
	if c.callback != nil {
		c.callback(c.total, c.total, 0)
	}
}

// IsBatch implements Displayer interface
func (c *callbackDisplayer) IsBatch() bool {
	return false
}
