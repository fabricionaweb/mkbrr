package torrent

import (
	"fmt"
	"time"

	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"
)

// modifyTorrentV2 modifies v2-only torrents using map-based manipulation
// to preserve FileTree structure. Returns (wasModified, error).
func modifyTorrentV2(mi *metainfo.MetaInfo, opts ModifyOptions) (bool, error) {
	wasModified := false

	// Apply flag-based overrides using map-based manipulation
	infoMap := make(map[string]interface{})
	if err := bencode.Unmarshal(mi.InfoBytes, &infoMap); err != nil {
		return false, fmt.Errorf("could not unmarshal info dict: %w", err)
	}

	// Track if we need to re-marshal
	needsRemarshal := false

	// Update tracker if flag provided
	if len(opts.TrackerURLs) > 0 {
		mi.Announce = opts.TrackerURLs[0]
		announceList := make([][]string, len(opts.TrackerURLs))
		for i, tracker := range opts.TrackerURLs {
			announceList[i] = []string{tracker}
		}
		mi.AnnounceList = announceList
		if !wasModified {
			wasModified = true
		}
	}

	// Update web seeds if provided via flag
	if len(opts.WebSeeds) > 0 {
		mi.UrlList = opts.WebSeeds
		if !wasModified {
			wasModified = true
		}
	}

	// Update comment if provided via flag
	if opts.Comment != "" && mi.Comment != opts.Comment {
		mi.Comment = opts.Comment
		if !wasModified {
			wasModified = true
		}
	}

	// Update private flag if provided via flag
	if opts.IsPrivate != nil {
		currentPrivate, hasPrivate := infoMap["private"]
		needsUpdate := false

		if !hasPrivate {
			needsUpdate = true
		} else {
			// Check if current value differs
			if currentVal, ok := currentPrivate.(int64); ok {
				if (*opts.IsPrivate && currentVal != 1) || (!*opts.IsPrivate && currentVal != 0) {
					needsUpdate = true
				}
			} else if currentVal, ok := currentPrivate.(int); ok {
				if (*opts.IsPrivate && currentVal != 1) || (!*opts.IsPrivate && currentVal != 0) {
					needsUpdate = true
				}
			} else {
				// Type mismatch, update anyway
				needsUpdate = true
			}
		}

		if needsUpdate {
			if *opts.IsPrivate {
				infoMap["private"] = int64(1)
			} else {
				infoMap["private"] = int64(0)
			}
			needsRemarshal = true
			if !wasModified {
				wasModified = true
			}
		}
	}

	// Update source if provided via flag
	if opts.Source != "" {
		currentSource, _ := infoMap["source"].(string)
		if currentSource != opts.Source {
			infoMap["source"] = opts.Source
			needsRemarshal = true
			if !wasModified {
				wasModified = true
			}
		}
	}

	// Add random entropy field for cross-seeding if enabled
	if opts.Entropy {
		if _, exists := infoMap["entropy"]; !exists {
			if entropy, err := generateRandomString(); err == nil {
				infoMap["entropy"] = entropy
				needsRemarshal = true
				if !wasModified {
					wasModified = true
				}
			}
		}
	}

	// Re-marshal info dict if needed
	if needsRemarshal {
		if infoBytes, err := bencode.Marshal(infoMap); err == nil {
			mi.InfoBytes = infoBytes
		} else {
			return false, fmt.Errorf("could not marshal modified info dict: %w", err)
		}
	}

	// Handle creator (outside info dict, so safe to modify directly)
	if opts.NoCreator {
		mi.CreatedBy = ""
		if !wasModified {
			wasModified = true
		}
	}

	// Update creation date
	if opts.NoDate {
		mi.CreationDate = 0
		if !wasModified {
			wasModified = true
		}
	} else {
		mi.CreationDate = time.Now().Unix()
		if !wasModified {
			wasModified = true
		}
	}

	return wasModified, nil
}
