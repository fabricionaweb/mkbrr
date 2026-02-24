package torrent

import (
	"crypto/sha1"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/anacrolix/torrent/merkle"
)

type pieceHasher struct {
	startTime  time.Time
	lastUpdate time.Time
	display    Displayer
	bufferPool *sync.Pool
	pieces     [][]byte
	files      []fileEntry
	pieceLen   int64
	numPieces  int
	readSize   int

	bytesProcessed          int64
	mutex                   sync.RWMutex
	failOnSeasonPackWarning bool
}

// optimizeForWorkload determines optimal read buffer size and number of worker goroutines
// based on the characteristics of input files (size and count). It considers:
// - single vs multiple files
// - average file size
// - system CPU count
// returns readSize (buffer size for reading) and numWorkers (concurrent goroutines)
func (h *pieceHasher) optimizeForWorkload() (int, int) {
	if len(h.files) == 0 {
		return 0, 0
	}

	// calculate total and maximum file sizes for optimization decisions
	var totalSize int64
	maxFileSize := int64(0)
	for _, f := range h.files {
		totalSize += f.length
		if f.length > maxFileSize {
			maxFileSize = f.length
		}
	}
	avgFileSize := totalSize / int64(len(h.files))

	var readSize, numWorkers int

	// optimize buffer size and worker count based on file characteristics
	switch {
	case len(h.files) == 1:
		if totalSize < 1<<20 {
			readSize = 64 << 10 // 64 KiB for very small files
			numWorkers = 1
		} else if totalSize < 1<<30 { // < 1 GiB
			readSize = 4 << 20 // 4 MiB
			numWorkers = runtime.NumCPU()
		} else {
			readSize = 8 << 20                // 8 MiB for large files
			numWorkers = runtime.NumCPU() * 2 // over-subscription for better I/O utilization
		}
	case avgFileSize < 1<<20: // avg < 1 MiB
		readSize = 256 << 10 // 256 KiB
		numWorkers = runtime.NumCPU()
	case avgFileSize < 10<<20: // avg < 10 MiB
		readSize = 1 << 20 // 1 MiB
		numWorkers = runtime.NumCPU()
	case avgFileSize < 1<<30: // avg < 1 GiB
		readSize = 4 << 20 // 4 MiB
		numWorkers = runtime.NumCPU() * 2
	default: // avg >= 1 GiB
		readSize = 8 << 20 // 8 MiB
		numWorkers = runtime.NumCPU() * 2
	}

	// ensure we don't create more workers than pieces to process
	if numWorkers > h.numPieces {
		numWorkers = h.numPieces
	}
	return readSize, numWorkers
}

// hashPieces coordinates the parallel hashing of all pieces in the torrent.
// It initializes a buffer pool, creates worker goroutines, and manages progress tracking.
// The pieces are distributed evenly across the specified number of workers.
// Returns an error if any worker encounters issues during hashing.
func (h *pieceHasher) hashPieces(numWorkers int) error {
	// Determine readSize and numWorkers. Use optimizeForWorkload if numWorkers isn't specified.
	if numWorkers <= 0 {
		h.readSize, numWorkers = h.optimizeForWorkload()
	} else {
		// If workers are specified, still need to determine readSize
		h.readSize, _ = h.optimizeForWorkload() // Only need readSize here
		// Ensure specified workers don't exceed pieces or minimum of 1
		if numWorkers > h.numPieces {
			numWorkers = h.numPieces
		}
		// Ensure at least 1 worker if pieces exist, even if user specified 0 somehow
		if h.numPieces > 0 && numWorkers <= 0 {
			numWorkers = 1
		}
	}

	// Final safeguard: Ensure at least one worker if there are pieces
	if h.numPieces > 0 && numWorkers <= 0 {
		numWorkers = 1
	}

	if numWorkers == 0 {
		// no workers needed, possibly no pieces to hash
		h.display.ShowProgress(0)
		h.display.FinishProgress()
		return nil
	}

	// initialize buffer pool
	h.bufferPool = &sync.Pool{
		New: func() interface{} {
			buf := make([]byte, h.readSize)
			return buf
		},
	}

	h.mutex.Lock()
	h.startTime = time.Now()
	h.lastUpdate = h.startTime
	h.mutex.Unlock()
	h.bytesProcessed = 0

	h.display.ShowFiles(h.files, numWorkers)

	seasonInfo := AnalyzeSeasonPack(h.files)

	h.display.ShowSeasonPackWarnings(seasonInfo)

	if seasonInfo.IsSuspicious && h.failOnSeasonPackWarning {
		return fmt.Errorf("season pack is suspicious, and --fail-on-season-warning is enabled")
	}

	var completedPieces uint64
	piecesPerWorker := (h.numPieces + numWorkers - 1) / numWorkers
	errorsCh := make(chan error, numWorkers)

	h.display.ShowProgress(h.numPieces)

	// spawn worker goroutines to process piece ranges in parallel
	var wg sync.WaitGroup
	for i := 0; i < numWorkers; i++ {
		start := i * piecesPerWorker
		end := min(start+piecesPerWorker, h.numPieces)

		wg.Add(1)
		go func(startPiece, endPiece int) {
			defer wg.Done()
			if err := h.hashPieceRange(startPiece, endPiece, &completedPieces); err != nil {
				errorsCh <- err
			}
		}(start, end)
	}

	// monitor and update progress bar in separate goroutine
	go func() {
		for {
			completed := atomic.LoadUint64(&completedPieces)
			if completed >= uint64(h.numPieces) {
				break
			}

			bytesProcessed := atomic.LoadInt64(&h.bytesProcessed)
			h.mutex.RLock()
			elapsed := time.Since(h.startTime).Seconds()
			h.mutex.RUnlock()
			var hashrate float64
			if elapsed > 0 {
				hashrate = float64(bytesProcessed) / elapsed
			}

			h.display.UpdateProgress(int(completed), hashrate)
			time.Sleep(200 * time.Millisecond)
		}
	}()

	wg.Wait()
	close(errorsCh)

	for err := range errorsCh {
		if err != nil {
			return err
		}
	}

	h.display.FinishProgress()
	return nil
}

// hashPieceRange processes and hashes a specific range of pieces assigned to a worker.
// It handles:
// - reading from multiple files that may span piece boundaries
// - maintaining file positions and readers
// - calculating SHA1 hashes for each piece
// - updating progress through the completedPieces counter
// Parameters:
//
//	startPiece: first piece index to process
//	endPiece: last piece index to process (exclusive)
//	completedPieces: atomic counter for progress tracking
func (h *pieceHasher) hashPieceRange(startPiece, endPiece int, completedPieces *uint64) error {
	// reuse buffer from pool to minimize allocations
	buf := h.bufferPool.Get().([]byte)
	defer h.bufferPool.Put(buf)

	hasher := sha1.New()
	// track open file handles to avoid reopening the same file
	readers := make(map[string]*fileReader)
	defer func() {
		for _, r := range readers {
			r.file.Close()
		}
	}()

	for pieceIndex := startPiece; pieceIndex < endPiece; pieceIndex++ {
		pieceOffset := int64(pieceIndex) * h.pieceLen
		pieceLength := h.pieceLen

		// handle last piece which may be shorter than others
		if pieceIndex == h.numPieces-1 {
			var totalLength int64
			for _, f := range h.files {
				totalLength += f.length
			}
			remaining := totalLength - pieceOffset
			if remaining < pieceLength {
				pieceLength = remaining
			}
		}

		hasher.Reset()
		remainingPiece := pieceLength

		for _, file := range h.files {
			// skip files that don't contain data for this piece
			if pieceOffset >= file.offset+file.length {
				continue
			}
			if remainingPiece <= 0 {
				break
			}

			// calculate read boundaries within the current file
			readStart := pieceOffset - file.offset
			if readStart < 0 {
				readStart = 0
			}

			readLength := file.length - readStart
			if readLength > remainingPiece {
				readLength = remainingPiece
			}

			// reuse or create new file reader
			reader, ok := readers[file.path]
			if !ok {
				f, err := os.OpenFile(file.path, os.O_RDONLY, 0)
				if err != nil {
					return fmt.Errorf("failed to open file %s: %w", file.path, err)
				}
				reader = &fileReader{
					file:     f,
					position: 0,
					length:   file.length,
				}
				readers[file.path] = reader
			}

			// ensure correct file position before reading
			if reader.position != readStart {
				if _, err := reader.file.Seek(readStart, 0); err != nil {
					return fmt.Errorf("failed to seek in file %s: %w", file.path, err)
				}
				reader.position = readStart
			}

			// read file data in chunks to avoid large memory allocations
			remaining := readLength
			for remaining > 0 {
				n := min(int(remaining), len(buf))

				read, err := io.ReadFull(reader.file, buf[:n])
				if err != nil && err != io.ErrUnexpectedEOF {
					return fmt.Errorf("failed to read file %s: %w", file.path, err)
				}

				hasher.Write(buf[:read])
				remaining -= int64(read)
				remainingPiece -= int64(read)
				reader.position += int64(read)
				pieceOffset += int64(read)

				atomic.AddInt64(&h.bytesProcessed, int64(read))
			}
		}

		// store piece hash and update progress
		h.pieces[pieceIndex] = hasher.Sum(nil)
		atomic.AddUint64(completedPieces, 1)
	}

	return nil
}

func NewPieceHasher(files []fileEntry, pieceLen int64, numPieces int, display Displayer, failOnSeasonPackWarning bool) *pieceHasher {
	bufferPool := &sync.Pool{
		New: func() interface{} {
			buf := make([]byte, pieceLen)
			return buf
		},
	}
	return &pieceHasher{
		pieces:                  make([][]byte, numPieces),
		pieceLen:                pieceLen,
		numPieces:               numPieces,
		files:                   files,
		display:                 display,
		bufferPool:              bufferPool,
		failOnSeasonPackWarning: failOnSeasonPackWarning,
	}
}

// fileHasher handles parallel hashing of files for v2 format
type fileHasher struct {
	files               []fileEntry
	results             []fileHash
	pieceLength         int64
	display             Displayer
	bytesProcessed      int64
	blocksPerPiece      int
	workers             int
	bufferPool          *sync.Pool
	failOnSeasonWarning bool
}

// fileHash stores hashing result for a single file
type fileHash struct {
	path        string
	length      int64
	piecesRoot  [32]byte
	pieceLayers string
}

// NewFileHasher creates a new file hasher for v2 format
func NewFileHasher(files []fileEntry, pieceLength int64, display Displayer, workers int, failOnSeasonWarning bool) *fileHasher {
	return &fileHasher{
		files:               files,
		pieceLength:         pieceLength,
		display:             display,
		workers:             workers,
		blocksPerPiece:      int(pieceLength / merkle.BlockSize),
		failOnSeasonWarning: failOnSeasonWarning,
	}
}

// hashFiles hashes all files in parallel using worker goroutines.
// This implements BEP 52 v2 format hashing which uses per-file merkle trees
// instead of global piece hashing. Each file is hashed independently, and
// files larger than the piece length have "piece layers" containing the
// merkle roots of each piece-sized chunk.
//
// The implementation uses static partitioning (like pieceHasher) where files
// are divided evenly among workers. This avoids channel overhead and provides
// better cache locality when processing large files.
//
// Progress is tracked at the block level (16 KiB chunks) since v2 hashing
// operates on merkle blocks rather than pieces. Total blocks are calculated
// upfront for accurate progress reporting.
func (h *fileHasher) hashFiles() error {
	numWorkers := h.workers
	if numWorkers <= 0 {
		numWorkers = h.optimizeWorkers()
	}
	if numWorkers > len(h.files) {
		numWorkers = len(h.files)
	}
	if numWorkers == 0 {
		numWorkers = 1
	}

	const blockSize = merkle.BlockSize
	h.bufferPool = &sync.Pool{
		New: func() any {
			return make([]byte, blockSize)
		},
	}

	h.display.ShowFiles(h.files, numWorkers)
	seasonInfo := AnalyzeSeasonPack(h.files)
	h.display.ShowSeasonPackWarnings(seasonInfo)
	if seasonInfo.IsSuspicious && h.failOnSeasonWarning {
		return fmt.Errorf("season pack is suspicious, and --fail-on-season-warning is enabled")
	}

	h.results = make([]fileHash, len(h.files))
	totalBlocks := int64(0)
	for _, f := range h.files {
		totalBlocks += (f.length + merkle.BlockSize - 1) / merkle.BlockSize
	}

	h.display.ShowProgress(int(totalBlocks))
	var completedBlocks uint64
	startTime := time.Now()

	// spawn worker goroutines to process file ranges in parallel (same pattern as pieceHasher)
	filesPerWorker := (len(h.files) + numWorkers - 1) / numWorkers
	var wg sync.WaitGroup
	errorsCh := make(chan error, numWorkers)

	for i := 0; i < numWorkers; i++ {
		start := i * filesPerWorker
		end := min(start+filesPerWorker, len(h.files))

		wg.Add(1)
		go func(startFile, endFile int) {
			defer wg.Done()
			for fileIdx := startFile; fileIdx < endFile; fileIdx++ {
				if err := h.hashFile(fileIdx, &completedBlocks); err != nil {
					errorsCh <- err
				}
			}
		}(start, end)
	}

	// Start a background goroutine to periodically update the progress display.
	// This runs concurrently with the worker goroutines and polls the atomic
	// counters to report progress every 200ms. It calculates the current hash
	// rate based on bytes processed and elapsed time for real-time feedback.
	go func() {
		for {
			completed := atomic.LoadUint64(&completedBlocks)
			if completed >= uint64(totalBlocks) {
				break
			}
			bytesProcessed := atomic.LoadInt64(&h.bytesProcessed)
			elapsed := time.Since(startTime).Seconds()
			var hashrate float64
			if elapsed > 0 {
				hashrate = float64(bytesProcessed) / elapsed
			}
			h.display.UpdateProgress(int(completed), hashrate)
			time.Sleep(200 * time.Millisecond)
		}
	}()

	wg.Wait()
	close(errorsCh)

	for err := range errorsCh {
		if err != nil {
			return err
		}
	}

	h.display.FinishProgress()
	return nil
}

// optimizeWorkers determines the optimal number of workers for v2 file hashing.
// Unlike piece hashing which processes pieces across files, v2 hashes entire
// files independently. This allows different optimization strategies:
//
// Single file: Use 1 worker for tiny files (<1MB), otherwise scale with CPU count.
//
//	Large single files benefit from parallel block hashing within the file.
//
// Multiple small files (<10MB avg): Limit workers to min(CPU, file_count).
//
//	Prevents overhead from too many goroutines when files are small.
//
// Multiple large files (>=10MB avg): Use up to 2*CPU workers.
//
//	Large files benefit from over-subscription for better I/O utilization,
//	especially when reading from multiple disks or network storage.
func (h *fileHasher) optimizeWorkers() int {
	if len(h.files) == 0 {
		return 1
	}
	var totalSize int64
	maxFileSize := int64(0)
	for _, f := range h.files {
		totalSize += f.length
		if f.length > maxFileSize {
			maxFileSize = f.length
		}
	}
	avgFileSize := totalSize / int64(len(h.files))
	switch {
	case len(h.files) == 1:
		if totalSize < 1<<20 {
			return 1
		}
		return runtime.NumCPU()
	case avgFileSize < 10<<20:
		return min(runtime.NumCPU(), len(h.files))
	default:
		return min(runtime.NumCPU()*2, len(h.files))
	}
}

// hashFile hashes a single file according to BEP 52 v2 format requirements.
// Each file is hashed independently using a merkle tree of 16 KiB blocks.
//
// Process:
//  1. Empty files: Return zero-filled piecesRoot (BEP 52 requirement)
//  2. Read file in 16 KiB blocks using buffer pool for memory efficiency
//  3. Hash each block with SHA-256 and feed to merkle hasher
//  4. Compute final merkle root (piecesRoot) for the file
//  5. For files larger than pieceLength: compute piece layers containing
//     the merkle roots of each piece-sized chunk
//
// The piecesRoot is used to verify file integrity, while pieceLayers enable
// verification of individual pieces during download without having the
// complete file. This is the key difference from v1's global piece hash.
func (h *fileHasher) hashFile(fileIdx int, completedBlocks *uint64) error {
	file := h.files[fileIdx]
	result := fileHash{
		path:   file.path,
		length: file.length,
	}

	// BEP 52: Empty files have an all-zero pieces root
	if file.length == 0 {
		result.piecesRoot = [32]byte{}
		h.results[fileIdx] = result
		return nil
	}

	f, err := os.Open(file.path)
	if err != nil {
		return fmt.Errorf("failed to open file %s: %w", file.path, err)
	}
	defer f.Close()

	// Process file in 16 KiB blocks (merkle block size)
	const blockSize = merkle.BlockSize
	hasher := merkle.NewHash()
	buf := h.bufferPool.Get().([]byte)
	defer h.bufferPool.Put(buf)

	var blockHashes [][32]byte

	for {
		n, err := f.Read(buf)
		if n > 0 {
			// Add block to merkle tree and store block hash for piece layers
			hasher.Write(buf[:n])
			blockHashes = append(blockHashes, sha256.Sum256(buf[:n]))
			atomic.AddInt64(&h.bytesProcessed, int64(n))
			atomic.AddUint64(completedBlocks, 1)
		}
		if err != nil {
			if err != io.EOF {
				return fmt.Errorf("failed to read file %s: %w", file.path, err)
			}
			break
		}
	}

	// Compute final merkle root for this file
	hasher.Sum(result.piecesRoot[:0])

	// Generate piece layers for multi-piece files
	// Piece layers allow verification of individual pieces without the full file
	if file.length > h.pieceLength && len(blockHashes) > 0 {
		result.pieceLayers = h.extractPieceLayers(blockHashes)
	}

	h.results[fileIdx] = result
	return nil
}

// extractPieceLayers generates the piece layer data for a multi-piece file.
//
// BEP 52 v2 format stores piece layers in the torrent's 'piece layers' dict,
// keyed by the file's piecesRoot. Each value is the concatenation of piece
// root hashes (32 bytes each) for that file.
//
// This allows clients to:
//  1. Verify downloaded pieces without having the complete file
//  2. Resume partial downloads by validating individual pieces
//  3. Cross-check piece integrity against the file's merkle tree
//
// The piece roots are computed by grouping block hashes into piece-sized
// chunks and computing the merkle root of each group. Incomplete pieces
// are padded with zero hashes per BEP 52 specification.
func (h *fileHasher) extractPieceLayers(blockHashes [][32]byte) string {
	pieceLayerHashes := make([][32]byte, 0)
	numPieces := (len(blockHashes) + h.blocksPerPiece - 1) / h.blocksPerPiece

	for pieceIdx := range numPieces {
		startBlock := pieceIdx * h.blocksPerPiece
		endBlock := min(startBlock+h.blocksPerPiece, len(blockHashes))

		pieceBlocks := blockHashes[startBlock:endBlock]
		if len(pieceBlocks) == 0 {
			break
		}

		// Compute merkle root for this piece's blocks
		// RootWithPadHash handles padding for incomplete pieces
		pieceRoot := merkle.RootWithPadHash(pieceBlocks, [32]byte{})
		pieceLayerHashes = append(pieceLayerHashes, pieceRoot)
	}

	// Concatenate all piece root hashes into a single byte string
	// Each hash is exactly 32 bytes (SHA-256 output size)
	var result strings.Builder
	result.Grow(len(pieceLayerHashes) * 32)
	for _, h := range pieceLayerHashes {
		result.Write(h[:])
	}

	return result.String()
}
