package torrent

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/anacrolix/torrent/merkle"
	"github.com/anacrolix/torrent/metainfo"
)

// v2Verifier handles verification of v2 format torrents
type v2Verifier struct {
	startTime   time.Time
	torrentInfo *metainfo.Info
	display     *Display
	contentPath string

	// v2 uses per-file verification
	files         []v2FileEntry
	badFiles      []string
	missingFiles  []string
	goodFiles     uint64
	badFilesCount uint64
	mutex         sync.RWMutex
	bufferPool    *sync.Pool

	pieceLen int64
	tracker  *progressTracker // Block-level progress tracking
}

// v2FileEntry represents a file in v2 format with its expected hash
type v2FileEntry struct {
	path       string
	relPath    string // Relative path from torrent root
	length     int64
	piecesRoot [32]byte
}

// VerifyDataV2 checks the integrity of content files against a v2 torrent file.
// It compares the actual file data against the per-file merkle roots in the torrent.
func VerifyDataV2(opts VerifyOptions, mi *metainfo.MetaInfo, info *metainfo.Info) (*VerificationResult, error) {
	verifier := &v2Verifier{
		torrentInfo: info,
		contentPath: opts.ContentPath,
		display:     NewDisplay(NewFormatter(opts.Verbose)),
		pieceLen:    info.PieceLength,
		bufferPool: &sync.Pool{
			New: func() interface{} {
				return make([]byte, merkle.BlockSize) // 16KB block size
			},
		},
	}
	verifier.display.SetQuiet(opts.Quiet)

	// Check if contentPath is a file or directory
	contentInfo, err := os.Stat(opts.ContentPath)
	if err != nil {
		return nil, fmt.Errorf("could not stat content path %q: %w", opts.ContentPath, err)
	}
	isSingleFile := !contentInfo.IsDir()

	// Build file list from v2 FileTree
	if err := verifier.buildFileList(isSingleFile); err != nil {
		return nil, err
	}

	// Map files to content directory
	if err := verifier.mapFilesToContent(); err != nil {
		return nil, err
	}

	// Calculate total pieces and blocks for result reporting
	totalPieces := int((info.TotalLength() + info.PieceLength - 1) / info.PieceLength)
	totalBlocks := int64(0)
	for _, f := range verifier.files {
		totalBlocks += (f.length + merkle.BlockSize - 1) / merkle.BlockSize
	}

	// Calculate workers BEFORE ShowFiles (same pattern as hasher_v2.go)
	numWorkers := opts.Workers
	if numWorkers <= 0 {
		numWorkers = verifier.optimizeWorkers()
	}
	if numWorkers > len(verifier.files) {
		numWorkers = len(verifier.files)
	}

	// Check if any files will use parallel block hashing (same as hasher_v2.go)
	blockWorkers := 0
	for _, f := range verifier.files {
		if f.length >= minParallelFileSize {
			blockWorkers = runtime.NumCPU()
			break
		}
	}

	// Show files being verified
	files := make([]fileEntry, len(verifier.files))
	for i, f := range verifier.files {
		files[i] = fileEntry{
			path:   f.path,
			length: f.length,
		}
	}
	verifier.display.ShowFiles(files, numWorkers, blockWorkers)
	verifier.display.ShowProgress(int(totalBlocks))

	// Initialize block-level progress tracker
	verifier.tracker = &progressTracker{}

	// Perform verification with calculated workers
	if err := verifier.verifyFiles(numWorkers); err != nil {
		return nil, fmt.Errorf("verification failed: %w", err)
	}

	// Calculate results
	result := &VerificationResult{
		TotalPieces:     totalPieces,
		GoodPieces:      int(verifier.goodFiles), // In v2, we count good files as "pieces"
		BadPieces:       int(verifier.badFilesCount),
		MissingPieces:   len(verifier.missingFiles),
		BadPieceIndices: []int{}, // v2 doesn't have piece indices like v1
		MissingFiles:    verifier.missingFiles,
	}

	// Calculate completion percentage
	totalFiles := len(verifier.files) + len(verifier.missingFiles)
	if totalFiles > 0 {
		result.Completion = (float64(verifier.goodFiles) / float64(totalFiles)) * 100.0
	}

	return result, nil
}

// buildFileList walks the FileTree and builds a list of files with their expected hashes
func (v *v2Verifier) buildFileList(isSingleFile bool) error {
	// For single-file torrents, the contentPath IS the file itself
	if isSingleFile && len(v.torrentInfo.FileTree.Dir) == 1 {
		for _, entry := range v.torrentInfo.FileTree.Dir {
			if entry.File.Length > 0 {
				// Single-file torrent: the entry IS the file
				// Use empty relPath since contentPath points to the file itself
				var piecesRoot [32]byte
				copy(piecesRoot[:], entry.File.PiecesRoot)
				v.files = append(v.files, v2FileEntry{
					relPath:    "", // Empty because contentPath is the file itself
					length:     entry.File.Length,
					piecesRoot: piecesRoot,
				})
				return nil
			}
		}
	}
	// Multi-file torrent: walk the tree normally
	return v.walkFileTree(v.torrentInfo.FileTree, "")
}

// walkFileTree recursively walks the FileTree structure
func (v *v2Verifier) walkFileTree(ft metainfo.FileTree, currentPath string) error {
	// Get sorted directory entries for deterministic order
	dirs := make([]string, 0, len(ft.Dir))
	for name := range ft.Dir {
		dirs = append(dirs, name)
	}
	sort.Strings(dirs)

	for _, name := range dirs {
		entry := ft.Dir[name]
		fullPath := filepath.Join(currentPath, name)

		if entry.File.Length > 0 {
			// It's a file
			// Validate piecesRoot length (must be 32 bytes for SHA-256)
			if len(entry.File.PiecesRoot) != 32 {
				return fmt.Errorf("invalid pieces_root length for %s: expected 32, got %d",
					fullPath, len(entry.File.PiecesRoot))
			}
			var piecesRoot [32]byte
			copy(piecesRoot[:], entry.File.PiecesRoot)

			v.files = append(v.files, v2FileEntry{
				relPath:    fullPath,
				length:     entry.File.Length,
				piecesRoot: piecesRoot,
			})
		} else if entry.Dir != nil {
			// It's a directory, recurse
			if err := v.walkFileTree(entry, fullPath); err != nil {
				return err
			}
		}
	}
	return nil
}

// mapFilesToContent maps torrent file paths to actual files on disk
func (v *v2Verifier) mapFilesToContent() error {
	baseContentPath := filepath.Clean(v.contentPath)

	for i := range v.files {
		// Try to find the file
		expectedPath := filepath.Join(baseContentPath, v.files[i].relPath)

		info, err := os.Stat(expectedPath)
		if err != nil {
			if os.IsNotExist(err) {
				v.missingFiles = append(v.missingFiles, v.files[i].relPath)
				continue
			}
			return fmt.Errorf("could not stat file %q: %w", expectedPath, err)
		}

		if info.IsDir() {
			return fmt.Errorf("expected file %q, but found a directory", expectedPath)
		}

		if info.Size() != v.files[i].length {
			v.missingFiles = append(v.missingFiles, v.files[i].relPath+" (size mismatch)")
			continue
		}

		v.files[i].path = expectedPath
	}

	// Filter out missing files from the verification list
	validFiles := make([]v2FileEntry, 0, len(v.files))
	for _, f := range v.files {
		if f.path != "" {
			validFiles = append(validFiles, f)
		}
	}
	v.files = validFiles

	return nil
}

// verifyFiles verifies all files using parallel workers
func (v *v2Verifier) verifyFiles(numWorkersOverride int) error {
	if len(v.files) == 0 {
		return nil
	}

	numWorkers := numWorkersOverride
	if numWorkers <= 0 {
		numWorkers = v.optimizeWorkers()
	}
	if numWorkers > len(v.files) {
		numWorkers = len(v.files)
	}

	v.startTime = time.Now()

	var wg sync.WaitGroup
	filesChan := make(chan int, len(v.files))
	errorsChan := make(chan error, numWorkers)
	doneChan := make(chan struct{})

	// Queue all files
	for i := range v.files {
		filesChan <- i
	}
	close(filesChan)

	// Calculate total blocks for progress tracking
	totalBlocks := int64(0)
	for _, f := range v.files {
		totalBlocks += (f.length + merkle.BlockSize - 1) / merkle.BlockSize
	}

	// Start progress monitoring (block-level like hasher_v2)
	go func() {
		defer close(doneChan)
		ticker := time.NewTicker(200 * time.Millisecond)
		defer ticker.Stop()
		for range ticker.C {
			completed := atomic.LoadUint64(&v.tracker.completed)
			elapsed := time.Since(v.startTime).Seconds()
			var rate float64
			if elapsed > 0 {
				bytesProcessed := atomic.LoadInt64(&v.tracker.bytesProcessed)
				rate = float64(bytesProcessed) / elapsed
			}
			v.display.UpdateProgress(int(completed), rate)

			if int64(completed) >= totalBlocks {
				return
			}
		}
	}()

	// Start workers
	for i := 0; i < numWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for fileIdx := range filesChan {
				if err := v.verifyFile(fileIdx); err != nil {
					errorsChan <- err
				}
			}
		}()
	}

	wg.Wait()
	close(errorsChan)

	// Wait for progress goroutine to finish
	<-doneChan

	for err := range errorsChan {
		if err != nil {
			return err
		}
	}

	return nil
}

// verifyFile verifies a single file by computing its merkle root
func (v *v2Verifier) verifyFile(fileIdx int) error {
	file := v.files[fileIdx]

	// Compute merkle root for the file
	computedRoot, err := v.computeFileMerkleRoot(file.path, file.length, file.piecesRoot)
	if err != nil {
		// Log error for debugging but don't fail the entire verification
		fmt.Fprintf(os.Stderr, "Error verifying file %s: %v\n", file.relPath, err)
		atomic.AddUint64(&v.badFilesCount, 1)
		v.mutex.Lock()
		v.badFiles = append(v.badFiles, file.relPath)
		v.mutex.Unlock()
		return nil // Don't stop on single file error
	}

	// Compare with expected root
	if bytes.Equal(computedRoot[:], file.piecesRoot[:]) {
		atomic.AddUint64(&v.goodFiles, 1)
	} else {
		atomic.AddUint64(&v.badFilesCount, 1)
		v.mutex.Lock()
		v.badFiles = append(v.badFiles, file.relPath)
		v.mutex.Unlock()
	}

	return nil
}

// optimizeWorkers determines the optimal number of workers for v2 file verification.
// Similar to hasher_v2.go, but adapted for verification workload.
func (v *v2Verifier) optimizeWorkers() int {
	if len(v.files) == 0 {
		return 1
	}

	var totalSize int64
	maxFileSize := int64(0)
	for _, f := range v.files {
		totalSize += f.length
		if f.length > maxFileSize {
			maxFileSize = f.length
		}
	}
	avgFileSize := totalSize / int64(len(v.files))

	// If we have very large files that will use parallel hashing internally,
	// we don't need as many file-level workers
	hasLargeFiles := maxFileSize >= minParallelFileSize

	switch {
	case len(v.files) == 1:
		// Single file - use 1 worker (internal parallelization handles large files)
		return 1
	case hasLargeFiles:
		// Large files use internal parallelization, so limit file-level workers
		return min(runtime.NumCPU(), len(v.files))
	case avgFileSize < 10<<20: // < 10MB average
		// Many small files - limit workers to reduce overhead
		return min(runtime.NumCPU(), len(v.files))
	default:
		// Multiple large files - can benefit from more workers
		return min(runtime.NumCPU()*2, len(v.files))
	}
}

// computeFileMerkleRoot computes the SHA-256 merkle root for a file
func (v *v2Verifier) computeFileMerkleRoot(filePath string, fileLength int64, expectedRoot [32]byte) ([32]byte, error) {
	var result [32]byte

	// Handle empty file
	if fileLength == 0 {
		// Empty files should have zero-filled piecesRoot
		var zeroRoot [32]byte
		if expectedRoot != zeroRoot {
			return result, fmt.Errorf("empty file expected zero hash, got non-zero")
		}
		return zeroRoot, nil
	}

	numPieces := int((fileLength + v.pieceLen - 1) / v.pieceLen)

	// For single-piece files, use the simple streaming path
	if numPieces <= 1 {
		return v.computeSinglePieceMerkleRoot(filePath, fileLength)
	}

	// For multi-piece files, choose based on size
	if fileLength >= minParallelFileSize {
		// Parallel path for large files
		return v.computeMultiPieceMerkleRootParallel(filePath, fileLength, numPieces)
	}

	// Sequential path for smaller files
	return v.computeMultiPieceMerkleRootSequential(filePath, fileLength, numPieces)
}

// computeSinglePieceMerkleRoot computes merkle root for a single-piece file using buffered I/O
func (v *v2Verifier) computeSinglePieceMerkleRoot(filePath string, fileLength int64) ([32]byte, error) {
	var result [32]byte

	f, err := os.Open(filePath)
	if err != nil {
		return result, fmt.Errorf("failed to open file: %w", err)
	}
	defer f.Close()

	hasher := merkle.NewHash()
	reader := bufio.NewReaderSize(f, readBufferSize)
	buf := v.bufferPool.Get().([]byte)
	defer v.bufferPool.Put(buf)

	var bytesRead int64
	for bytesRead < fileLength {
		toRead := min(fileLength - bytesRead, int64(len(buf)))

		n, err := reader.Read(buf[:toRead])
		if n > 0 {
			if _, writeErr := hasher.Write(buf[:n]); writeErr != nil {
				return result, fmt.Errorf("failed to hash data: %w", writeErr)
			}
			bytesRead += int64(n)
			v.tracker.AddBlocks(1, int64(n))
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return result, fmt.Errorf("failed to read file: %w", err)
		}
	}

	hasher.Sum(result[:0])
	return result, nil
}

// computeMultiPieceMerkleRootSequential computes merkle root for a multi-piece file using sequential processing with buffered I/O
func (v *v2Verifier) computeMultiPieceMerkleRootSequential(filePath string, fileLength int64, numPieces int) ([32]byte, error) {
	var result [32]byte
	const blockSize = merkle.BlockSize // 16 KiB
	blocksPerPiece := int(v.pieceLen / blockSize)

	f, err := os.Open(filePath)
	if err != nil {
		return result, fmt.Errorf("failed to open file: %w", err)
	}
	defer f.Close()

	reader := bufio.NewReaderSize(f, readBufferSize)
	buf := v.bufferPool.Get().([]byte)
	defer v.bufferPool.Put(buf)

	pieceRoots := make([][32]byte, 0, numPieces)
	blockHashes := make([][32]byte, 0, blocksPerPiece)

	var bytesRead int64

	for bytesRead < fileLength {
		toRead := blockSize
		if fileLength-bytesRead < int64(toRead) {
			toRead = int(fileLength - bytesRead)
		}
		n, err := reader.Read(buf[:toRead])
		if n > 0 {
			blockHash := sha256.Sum256(buf[:n])
			blockHashes = append(blockHashes, blockHash)

			bytesRead += int64(n)
			v.tracker.AddBlocks(1, int64(n))

			if len(blockHashes) == blocksPerPiece {
				pieceRoot := merkle.RootWithPadHash(blockHashes, metainfo.HashForPiecePad(v.pieceLen))
				pieceRoots = append(pieceRoots, pieceRoot)
				blockHashes = blockHashes[:0]
			}
		}

		if err == io.EOF {
			break
		}
		if err != nil {
			return result, fmt.Errorf("failed to read file: %w", err)
		}
	}

	if len(blockHashes) > 0 {
		pieceRoot := merkle.RootWithPadHash(blockHashes, metainfo.HashForPiecePad(v.pieceLen))
		pieceRoots = append(pieceRoots, pieceRoot)
	}

	piecePadHash := metainfo.HashForPiecePad(v.pieceLen)
	result = merkle.RootWithPadHash(pieceRoots, piecePadHash)

	return result, nil
}

// computeMultiPieceMerkleRootParallel computes merkle root for a large multi-piece file using parallel workers
func (v *v2Verifier) computeMultiPieceMerkleRootParallel(filePath string, fileLength int64, numPieces int) ([32]byte, error) {
	var result [32]byte
	const blockSize = merkle.BlockSize
	blocksPerPiece := int(v.pieceLen / blockSize)
	pieceSize := int64(blocksPerPiece * blockSize)

	numWorkers := runtime.NumCPU()

	chunkPieces := max(int((targetChunkSize + pieceSize - 1) / pieceSize), 1)
	chunkSize := int64(chunkPieces) * pieceSize

	totalChunks := int((fileLength + chunkSize - 1) / chunkSize)
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
				endOffset := min(startOffset+chunkSize, fileLength)

				blocks, bytesRead, err := v.hashChunk(filePath, startOffset, endOffset)
				if err != nil {
					select {
					case errCh <- err:
					default:
					}
					return
				}
				chunkBlocks[chunkIdx] = blocks
				// Update progress for all blocks in this chunk
				v.tracker.AddBlocks(len(blocks), bytesRead)
			}
		}(i)
	}

	wg.Wait()

	select {
	case err := <-errCh:
		return result, err
	default:
	}

	// Concatenate all block hashes in order
	numBlocks := int((fileLength + blockSize - 1) / blockSize)
	allBlocks := make([][32]byte, 0, numBlocks)
	for _, blocks := range chunkBlocks {
		allBlocks = append(allBlocks, blocks...)
	}

	if len(allBlocks) != numBlocks {
		return result, fmt.Errorf("block count mismatch: expected %d blocks, got %d", numBlocks, len(allBlocks))
	}

	// Compute piece roots from block hashes
	piecePadHash := metainfo.HashForPiecePad(v.pieceLen)
	pieceRoots := make([][32]byte, 0, numPieces)
	for i := 0; i < len(allBlocks); i += blocksPerPiece {
		end := min(i+blocksPerPiece, len(allBlocks))
		pieceRoots = append(pieceRoots, merkle.RootWithPadHash(allBlocks[i:end], piecePadHash))
	}

	// Compute final merkle root from piece roots
	result = merkle.RootWithPadHash(pieceRoots, piecePadHash)

	return result, nil
}

// hashChunk reads and hashes a chunk of a file, returning all block hashes
func (v *v2Verifier) hashChunk(filePath string, startOffset, endOffset int64) ([][32]byte, int64, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()

	if _, err := f.Seek(startOffset, io.SeekStart); err != nil {
		return nil, 0, err
	}

	const blockSize = merkle.BlockSize
	reader := bufio.NewReaderSize(f, readBufferSize)
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

		if err == io.EOF || err == io.ErrUnexpectedEOF {
			break
		}
		if err != nil {
			return nil, bytesRead, err
		}
	}

	return blocks, bytesRead, nil
}
