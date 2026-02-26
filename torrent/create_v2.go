package torrent

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"
)

// createV2 creates a v2-only torrent (BEP 52)
func createV2(opts CreateOptions) (*Torrent, error) {
	path := filepath.ToSlash(opts.Path)
	name := opts.Name
	if name == "" {
		// preserve the folder name even for single-file torrents
		name = filepath.Base(filepath.Clean(path))
	}

	mi := &metainfo.MetaInfo{
		Comment: opts.Comment,
	}

	// Set tracker information
	if len(opts.TrackerURLs) > 0 {
		mi.Announce = opts.TrackerURLs[0]
		if len(opts.TrackerURLs) > 1 {
			announceList := make([][]string, len(opts.TrackerURLs))
			for i, tracker := range opts.TrackerURLs {
				announceList[i] = []string{tracker}
			}
			mi.AnnounceList = announceList
		}
	}

	if !opts.NoCreator {
		mi.CreatedBy = fmt.Sprintf("mkbrr/%s (https://github.com/autobrr/mkbrr)", opts.Version)
	}

	if !opts.NoDate {
		mi.CreationDate = time.Now().Unix()
	}

	files := make([]fileEntry, 0, 1)
	var totalSize int64
	var baseDir string
	originalPaths := make(map[string]string) // map resolved path -> original path for metainfo

	err := filepath.Walk(path, func(currentPath string, walkInfo os.FileInfo, walkErr error) error {
		if walkErr != nil {
			// check if the error is due to a broken symlink during walk
			// if lstat works but stat fails, it's likely a broken link we might handle later
			if _, lerr := os.Lstat(currentPath); lerr == nil {
				// we can lstat it, maybe it's a broken link we can ignore?
				// for now, let's return the original error to maintain behavior.
				// consider adding verbose logging here if needed.
			}
			return walkErr
		}

		lstatInfo, err := os.Lstat(currentPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: could not lstat %q: %v\n", currentPath, err)
			return nil
		}

		resolvedPath := currentPath
		resolvedInfo := lstatInfo

		// check if it's a symlink
		if lstatInfo.Mode()&os.ModeSymlink != 0 {
			linkTarget, err := os.Readlink(currentPath)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Warning: could not readlink %q: %v\n", currentPath, err)
				return nil
			}
			// if link is relative, resolve it based on the link's directory
			if !filepath.IsAbs(linkTarget) {
				linkTarget = filepath.Join(filepath.Dir(currentPath), linkTarget)
			}
			resolvedPath = filepath.Clean(linkTarget)

			// stat target
			statInfo, err := os.Stat(resolvedPath)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Warning: could not stat symlink target %q for link %q: %v\n", resolvedPath, currentPath, err)
				return nil // skip broken link or inaccessible target
			}
			resolvedInfo = statInfo
		}

		if resolvedInfo.IsDir() {
			if shouldIgnoreDir(currentPath) || shouldIgnoreDir(resolvedPath) {
				return filepath.SkipDir
			}

			if baseDir == "" && currentPath == path { // only set baseDir for the initial path if it's a dir
				baseDir = currentPath
			}
			return nil
		}

		// it's a file (or a link pointing to one)
		shouldIgnore, err := shouldIgnoreFile(currentPath, opts.ExcludePatterns, opts.IncludePatterns) // ignore based on original path
		if err != nil {
			return fmt.Errorf("error processing file patterns for %q: %w", currentPath, err)
		}
		if shouldIgnore {
			return nil
		}

		// add the file using the resolved path for hashing, but store the original path for metainfo
		files = append(files, fileEntry{
			path:   resolvedPath, // use the actual content path for hashing
			length: resolvedInfo.Size(),
			offset: totalSize,
		})
		originalPaths[resolvedPath] = currentPath
		totalSize += resolvedInfo.Size()
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("error walking path: %w", err)
	}

	// sort files to ensure consistent order
	sort.Slice(files, func(i, j int) bool {
		return files[i].path < files[j].path
	})

	// recalculate offsets based on the sorted file order
	// context: https://github.com/autobrr/mkbrr/issues/64
	var currentOffset int64 = 0
	for i := range files {
		files[i].offset = currentOffset
		currentOffset += files[i].length
	}

	if totalSize == 0 {
		return nil, fmt.Errorf("input path %q contains no files or only empty files, cannot create torrent", path)
	}

	// Function to create torrent with given piece length
	createWithPieceLength := func(pieceLength uint) (*Torrent, error) {
		pieceLenInt := int64(1) << pieceLength

		var display Displayer
		if opts.ProgressCallback != nil {
			// Use callback displayer when progress callback is provided
			display = &callbackDisplayer{callback: opts.ProgressCallback}
		} else {
			// Use default display when no callback is provided
			defaultDisplay := NewDisplay(NewFormatter(opts.Verbose || opts.InfoOnly))
			defaultDisplay.SetQuiet(opts.Quiet || opts.InfoOnly)
			display = defaultDisplay
		}

		hasher := NewFileHasher(files, pieceLenInt, display, opts.Workers, opts.FailOnSeasonPackWarning)
		// Pass the specified or default worker count from opts
		if err := hasher.hashFiles(); err != nil {
			return nil, err
		}
		fileTree := buildFileTree(files, hasher.results, baseDir, originalPaths)

		info := &metainfo.Info{
			Name:        name,
			PieceLength: pieceLenInt,
			Private:     &opts.IsPrivate,
			MetaVersion: 2,
			FileTree:    fileTree,
		}

		if opts.Source != "" {
			info.Source = opts.Source
		}

		pieceLayers := make(map[string]string)
		for _, result := range hasher.results {
			if result.pieceLayers != "" {
				pieceLayers[string(result.piecesRoot[:])] = result.pieceLayers
			}
		}

		infoBytes, err := bencode.Marshal(info)
		if err != nil {
			return nil, fmt.Errorf("error encoding info: %w", err)
		}

		// add random entropy field for cross-seeding if enabled
		if opts.Entropy {
			infoMap := make(map[string]interface{})
			if err := bencode.Unmarshal(infoBytes, &infoMap); err == nil {
				if entropy, err := generateRandomString(); err == nil {
					infoMap["entropy"] = entropy
					if infoBytes, err = bencode.Marshal(infoMap); err == nil {
						mi.InfoBytes = infoBytes
					}
				}
			}
		} else {
			mi.InfoBytes = infoBytes
		}

		if len(pieceLayers) > 0 {
			mi.PieceLayers = pieceLayers
		}

		if len(opts.WebSeeds) > 0 {
			mi.UrlList = opts.WebSeeds
		}

		return &Torrent{mi}, nil
	}

	var pieceLength uint
	if opts.PieceLengthExp == nil {
		pieceLength = calculatePieceLengthV2(totalSize, opts.MaxPieceLength)
	} else {
		pieceLength = *opts.PieceLengthExp

		maxExp := uint(27)
		if pieceLength < 14 || pieceLength > maxExp {
			return nil, fmt.Errorf("piece length exponent for v2 must be between 14 (16 KiB) and %d (%d MiB), got: %d",
				maxExp, 1<<(maxExp-20), pieceLength)
		}
	}

	return createWithPieceLength(pieceLength)
}

// calculatePieceLengthV2 calculates piece length for v2 torrents
// Unlike v1, v2 uses merkle trees per-file, so piece length rules are simpler
// No PrivateTracker as now uses it, so dont need to check pieces table
func calculatePieceLengthV2(totalSize int64, maxPieceLength *uint) uint {
	minExp := uint(14) // v2 min: 14 (16 KiB) - BEP 52 requires piece length >= block size (16 KiB)
	maxExp := uint(27) // not a BEP requirement

	// validate maxPieceLength - if it's below v2 minimum, use minimum
	if maxPieceLength != nil {
		if *maxPieceLength < minExp {
			return minExp
		}
		maxExp = min(*maxPieceLength, maxExp)
	}

	// default calculation for automatic piece length (same ranges as v1)
	// ensure minimum of 1 byte for calculation
	size := max(totalSize, 1)

	var exp uint
	switch {
	case size <= 64<<20: // 0 to 64 MB: 32 KiB pieces (2^15)
		exp = 15
	case size <= 128<<20: // 64-128 MB: 64 KiB pieces (2^16)
		exp = 16
	case size <= 256<<20: // 128-256 MB: 128 KiB pieces (2^17)
		exp = 17
	case size <= 512<<20: // 256-512 MB: 256 KiB pieces (2^18)
		exp = 18
	case size <= 1024<<20: // 512 MB-1 GB: 512 KiB pieces (2^19)
		exp = 19
	case size <= 2048<<20: // 1-2 GB: 1 MiB pieces (2^20)
		exp = 20
	case size <= 4096<<20: // 2-4 GB: 2 MiB pieces (2^21)
		exp = 21
	case size <= 8192<<20: // 4-8 GB: 4 MiB pieces (2^22)
		exp = 22
	case size <= 16384<<20: // 8-16 GB: 8 MiB pieces (2^23)
		exp = 23
	case size <= 32768<<20: // 16-32 GB: 16 MiB pieces (2^24)
		exp = 24
	case size <= 65536<<20: // 32-64 GB: 32 MiB pieces (2^25)
		exp = 25
	case size <= 131072<<20: // 64-128 GB: 64 MiB pieces (2^26)
		exp = 26
	default: // above 128 GB: 128 MiB pieces (2^27)
		exp = 27
	}

	// for v2, we don't cap at 2^24 like v1 - v2 can go up to 2^27 (maxExp)
	// this is because v2 uses merkle trees and handles large pieces more efficiently

	// ensure we stay within bounds
	if exp < minExp {
		exp = minExp
	}
	if exp > maxExp {
		exp = maxExp
	}

	return exp
}

// buildFileTree builds the FileTree structure from file results
func buildFileTree(files []fileEntry, results []fileHash, baseDir string, originalPaths map[string]string) metainfo.FileTree {
	// Always build as directory structure per BEP 52
	// Single file: {filename: {"": {length, pieces root}}}
	// Multi-file: {dir: {filename: {"": {length, pieces root}}}}
	root := metainfo.FileTree{
		Dir: make(map[string]metainfo.FileTree),
	}

	for i, f := range files {
		// Use the original path (symlink) for metainfo if available
		originalPath := originalPaths[f.path]
		if originalPath == "" {
			originalPath = f.path
		}

		var parts []string
		if baseDir == "" {
			// Single file: just use the filename
			parts = []string{filepath.Base(originalPath)}
		} else {
			// Multi-file: use relative path from baseDir
			relPath, err := filepath.Rel(baseDir, originalPath)
			if err != nil {
				relPath = originalPath
			}
			relPath = filepath.ToSlash(relPath)
			parts = splitPath(relPath)
		}

		insertIntoFileTree(&root, parts, results[i])
	}

	return root
}

// insertIntoFileTree inserts a file into the FileTree structure
func insertIntoFileTree(tree *metainfo.FileTree, pathParts []string, result fileHash) {
	if len(pathParts) == 0 {
		return
	}

	if len(pathParts) == 1 {
		// BEP 52: file properties go under empty string key
		// anacrolix FileTree.MarshalBencode handles this automatically:
		// when File is set (not Dir), it marshals as {"": {length, pieces root}}
		tree.Dir[pathParts[0]] = metainfo.FileTree{
			File: metainfo.FileTreeFile{
				Length:     result.length,
				PiecesRoot: string(result.piecesRoot[:]),
			},
		}
	} else {
		dirName := pathParts[0]
		if tree.Dir[dirName].Dir == nil {
			tree.Dir[dirName] = metainfo.FileTree{
				Dir: make(map[string]metainfo.FileTree),
			}
		}
		subTree := tree.Dir[dirName]
		insertIntoFileTree(&subTree, pathParts[1:], result)
		tree.Dir[dirName] = subTree
	}
}

// splitPath splits a path into components (helper to avoid strings import)
func splitPath(path string) []string {
	if path == "" {
		return []string{}
	}
	parts := make([]string, 0)
	start := 0
	for i := 0; i < len(path); i++ {
		if path[i] == '/' {
			if i > start {
				parts = append(parts, path[start:i])
			}
			start = i + 1
		}
	}
	if start < len(path) {
		parts = append(parts, path[start:])
	}
	return parts
}
