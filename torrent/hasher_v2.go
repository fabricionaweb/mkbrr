package torrent

import (
	"bufio"
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

// progressTracker handles atomic progress updates
type progressTracker struct {
	completed      uint64
	bytesProcessed int64
	display        Displayer
	startTime      time.Time
}

func newProgressTracker(display Displayer, startTime time.Time) *progressTracker {
	return &progressTracker{display: display, startTime: startTime}
}

func (p *progressTracker) AddBlocks(count int, bytes int64) {
	atomic.AddUint64(&p.completed, uint64(count))
	atomic.AddInt64(&p.bytesProcessed, bytes)
}

func (p *progressTracker) Update(total int) {
	completed := atomic.LoadUint64(&p.completed)
	bytes := atomic.LoadInt64(&p.bytesProcessed)
	elapsed := time.Since(p.startTime).Seconds()
	if elapsed > 0 {
		p.display.UpdateProgress(int(completed), float64(bytes)/elapsed)
	}
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
	startTime := time.Now()
	tracker := newProgressTracker(h.display, startTime)

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
				if err := h.hashFile(fileIdx, tracker); err != nil {
					errorsCh <- err
				}
			}
		}(start, end)
	}

	// background goroutine to periodically update the progress display
	go func() {
		for {
			if atomic.LoadUint64(&tracker.completed) >= uint64(totalBlocks) {
				break
			}
			tracker.Update(int(totalBlocks))
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
//  2. For single-piece files: Stream through merkle.NewHash()
//  3. For multi-piece files: Choose sequential (small files) or parallel (large files) path
//  4. Compute piece roots from block hashes for piece layers
//
// The piecesRoot is used to verify file integrity, while pieceLayers enable
// verification of individual pieces during download without having the
// complete file.
func (h *fileHasher) hashFile(fileIdx int, tracker *progressTracker) error {
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

	const blockSize = merkle.BlockSize
	numPieces := int((file.length + h.pieceLength - 1) / h.pieceLength)

	// For single-piece files, use the simple streaming path
	if numPieces <= 1 {
		return h.hashFileSinglePiece(file, tracker, &result, fileIdx)
	}

	// For multi-piece files, choose based on size
	const minParallelSize = 100 << 20 // 100 MB threshold
	if file.length >= minParallelSize {
		// Parallel path opens its own file handles per worker
		return h.hashFileParallel(file, numPieces, tracker, &result, fileIdx)
	}

	// Sequential path for smaller files - open file here
	return h.hashFileSequential(file, numPieces, tracker, &result, fileIdx)
}

// hashFileSinglePiece hashes a single-piece file using the standard merkle hasher
func (h *fileHasher) hashFileSinglePiece(file fileEntry, tracker *progressTracker, result *fileHash, fileIdx int) error {
	f, err := os.Open(file.path)
	if err != nil {
		return fmt.Errorf("failed to open file %s: %w", file.path, err)
	}
	defer f.Close()

	reader := bufio.NewReaderSize(f, 4<<20) // 4MB buffer
	buf := h.bufferPool.Get().([]byte)
	defer h.bufferPool.Put(buf)

	hasher := merkle.NewHash()

	for {
		n, err := reader.Read(buf)
		if n > 0 {
			hasher.Write(buf[:n])
			tracker.AddBlocks(1, int64(n))
		}
		if err != nil {
			if err != io.EOF {
				return fmt.Errorf("failed to read file %s: %w", file.path, err)
			}
			break
		}
	}

	hasher.Sum(result.piecesRoot[:0])
	h.results[fileIdx] = *result
	return nil
}

// hashFileSequential hashes a file using single-threaded streaming (optimized for smaller files <100MB)
// Stores all block hashes in memory to compute both piecesRoot and pieceRoots in one pass.
func (h *fileHasher) hashFileSequential(file fileEntry, numPieces int, tracker *progressTracker, result *fileHash, fileIdx int) error {
	f, err := os.Open(file.path)
	if err != nil {
		return fmt.Errorf("failed to open file %s: %w", file.path, err)
	}
	defer f.Close()

	reader := bufio.NewReaderSize(f, 4<<20) // 4MB buffer
	buf := h.bufferPool.Get().([]byte)
	defer h.bufferPool.Put(buf)

	const blockSize = merkle.BlockSize
	blocksPerPiece := int(h.pieceLength / blockSize)
	numBlocks := int((file.length + blockSize - 1) / blockSize)

	// Store all block hashes to compute piecesRoot later
	allBlocks := make([][32]byte, 0, numBlocks)
	pieceRoots := make([][32]byte, 0, numPieces)
	blockHashes := make([][32]byte, 0, blocksPerPiece)

	for {
		n, err := reader.Read(buf)
		if n > 0 {
			blockHash := sha256.Sum256(buf[:n])
			allBlocks = append(allBlocks, blockHash)
			blockHashes = append(blockHashes, blockHash)

			if len(blockHashes) == blocksPerPiece {
				pieceRoot := merkle.RootWithPadHash(blockHashes, [32]byte{})
				pieceRoots = append(pieceRoots, pieceRoot)
				blockHashes = blockHashes[:0]
			}

			tracker.AddBlocks(1, int64(n))
		}
		if err != nil {
			if err != io.EOF {
				return fmt.Errorf("failed to read file %s: %w", file.path, err)
			}
			break
		}
	}

	if len(blockHashes) > 0 {
		pieceRoot := merkle.RootWithPadHash(blockHashes, [32]byte{})
		pieceRoots = append(pieceRoots, pieceRoot)
	}

	// Compute piecesRoot from all block hashes
	result.piecesRoot = merkle.RootWithPadHash(allBlocks, [32]byte{})

	var layers strings.Builder
	layers.Grow(len(pieceRoots) * 32)
	for _, root := range pieceRoots {
		layers.Write(root[:])
	}
	result.pieceLayers = layers.String()
	h.results[fileIdx] = *result
	return nil
}

// hashFileParallel hashes a large file using parallel workers for block hashing.
func (h *fileHasher) hashFileParallel(file fileEntry, numPieces int, tracker *progressTracker, result *fileHash, fileIdx int) error {
	const blockSize = merkle.BlockSize
	blocksPerPiece := int(h.pieceLength / blockSize)
	numBlocks := int((file.length + blockSize - 1) / blockSize)

	const targetChunkSize = 256 << 20
	pieceSize := int64(blocksPerPiece * blockSize)

	numWorkers := runtime.NumCPU()
	chunkPieces := int((targetChunkSize + pieceSize - 1) / pieceSize)
	if chunkPieces < 1 {
		chunkPieces = 1
	}
	chunkSize := int64(chunkPieces) * pieceSize

	totalChunks := int((file.length + chunkSize - 1) / chunkSize)
	if totalChunks < numWorkers {
		numWorkers = totalChunks
	}

	chunkBlocks := make([][][32]byte, totalChunks)
	errCh := make(chan error, 1)
	var wg sync.WaitGroup

	// Launch workers - each worker processes every numWorkers-th chunk
	for i := 0; i < numWorkers; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			for chunkIdx := workerID; chunkIdx < totalChunks; chunkIdx += numWorkers {
				startOffset := int64(chunkIdx) * chunkSize
				endOffset := min(startOffset+chunkSize, file.length)

				blocks, bytesRead, err := h.hashChunk(file.path, startOffset, endOffset)
				if err != nil {
					select {
					case errCh <- err:
					default:
					}
					return
				}
				chunkBlocks[chunkIdx] = blocks
				tracker.AddBlocks(len(blocks), bytesRead)
			}
		}(i)
	}

	wg.Wait()

	select {
	case err := <-errCh:
		return err
	default:
	}

	// Concatenate all block hashes in order
	allBlocks := make([][32]byte, 0, numBlocks)
	for _, blocks := range chunkBlocks {
		allBlocks = append(allBlocks, blocks...)
	}

	result.piecesRoot = merkle.RootWithPadHash(allBlocks, [32]byte{})

	pieceRoots := make([][32]byte, 0, numPieces)
	for i := 0; i < len(allBlocks); i += blocksPerPiece {
		end := min(i+blocksPerPiece, len(allBlocks))
		pieceRoots = append(pieceRoots, merkle.RootWithPadHash(allBlocks[i:end], [32]byte{}))
	}

	if len(pieceRoots) > 1 {
		var layers strings.Builder
		layers.Grow(len(pieceRoots) * 32)
		for _, root := range pieceRoots {
			layers.Write(root[:])
		}
		result.pieceLayers = layers.String()
	}

	h.results[fileIdx] = *result
	return nil
}

// hashChunk reads and hashes a chunk of a file, returning all block hashes
func (h *fileHasher) hashChunk(filePath string, startOffset, endOffset int64) ([][32]byte, int64, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()

	// Seek to start position
	if _, err := f.Seek(startOffset, io.SeekStart); err != nil {
		return nil, 0, err
	}

	const blockSize = merkle.BlockSize
	reader := bufio.NewReaderSize(f, 4<<20) // // 4MB buffer: balances syscall reduction with per-worker memory (4MB × numWorkers total)
	buf := make([]byte, blockSize)

	numBlocks := int((endOffset - startOffset + blockSize - 1) / blockSize)
	blocks := make([][32]byte, 0, numBlocks)

	var bytesRead int64
	remaining := endOffset - startOffset

	for remaining > 0 {
		toRead := blockSize
		if remaining < int64(blockSize) {
			toRead = int(remaining)
		}

		n, err := io.ReadFull(reader, buf[:toRead])
		if n > 0 {
			blocks = append(blocks, sha256.Sum256(buf[:n]))
			bytesRead += int64(n)
			remaining -= int64(n)
		}

		if err != nil {
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				break
			}
			return nil, 0, err
		}
	}

	return blocks, bytesRead, nil
}
