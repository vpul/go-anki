package apkg

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// ExportColpkgOptions configures how a .colpkg (collection package) file is created.
//
// A .colpkg is a ZIP archive containing:
//   - collection.anki21b: Zstandard-compressed SQLite database
//   - collection.anki2: Small placeholder file
//   - media: JSON file mapping media indices to filenames
//   - Numeric media files referenced by the media map
type ExportColpkgOptions struct {
	// SourceDB is the path to the collection.anki2 SQLite database.
	SourceDB string

	// OutputPath is where the .colpkg file will be written.
	OutputPath string

	// MediaDir is the directory containing media files.
	// If empty, no media files are included.
	MediaDir string

	// MediaMap maps media indices to filenames.
	// If nil and MediaDir is set, media files are auto-discovered.
	MediaMap MediaMap
}

// ExportColpkg creates a .colpkg file from an Anki collection database.
func ExportColpkg(opts ExportColpkgOptions) error {
	if opts.SourceDB == "" {
		return fmt.Errorf("source database path is required")
	}
	if opts.OutputPath == "" {
		return fmt.Errorf("output path is required")
	}
	if opts.MediaMap != nil && opts.MediaDir == "" {
		return fmt.Errorf("media map provided without media directory; set MediaDir to include media files")
	}

	// Read the source database
	dbData, err := os.ReadFile(opts.SourceDB)
	if err != nil {
		return fmt.Errorf("read source database: %w", err)
	}

	// Compress the database with zstd
	compressedDB, err := compressZstd(dbData)
	if err != nil {
		return fmt.Errorf("compress database: %w", err)
	}

	// Build media map
	var mediaMap MediaMap
	if opts.MediaMap != nil {
		mediaMap = opts.MediaMap
	} else if opts.MediaDir != "" {
		mediaMap, err = discoverMediaFiles(opts.MediaDir)
		if err != nil {
			return fmt.Errorf("discover media files: %w", err)
		}
	}

	// Ensure output directory exists
	if err := os.MkdirAll(filepath.Dir(opts.OutputPath), 0755); err != nil {
		return fmt.Errorf("create output directory: %w", err)
	}

	// Create the .colpkg ZIP file
	outFile, err := os.Create(opts.OutputPath)
	if err != nil {
		return fmt.Errorf("create output file: %w", err)
	}
	defer func() { _ = outFile.Close() }()

	zipWriter := zip.NewWriter(outFile)
	defer func() { _ = zipWriter.Close() }()

	// Add collection.anki21b (zstd-compressed)
	if err := addFileToZip(zipWriter, "collection.anki21b", compressedDB); err != nil {
		return fmt.Errorf("add compressed collection to zip: %w", err)
	}

	// Add collection.anki2 placeholder (empty file, as per Anki convention)
	if err := addFileToZip(zipWriter, "collection.anki2", []byte("")); err != nil {
		return fmt.Errorf("add placeholder to zip: %w", err)
	}

	// Add media map
	if mediaMap == nil {
		mediaMap = make(MediaMap)
	}
	mediaMapData, err := json.Marshal(mediaMap)
	if err != nil {
		return fmt.Errorf("marshal media map: %w", err)
	}
	if err := addFileToZip(zipWriter, "media", mediaMapData); err != nil {
		return fmt.Errorf("add media map to zip: %w", err)
	}

	// Add media files
	for idxStr, filename := range mediaMap {
		if err := validateMediaFilename(filename); err != nil {
			return fmt.Errorf("invalid media filename %q in map: %w", filename, err)
		}
		mediaPath := filepath.Join(opts.MediaDir, filename)
		if err := validatePathWithinDir(mediaPath, opts.MediaDir); err != nil {
			return fmt.Errorf("media path escapes directory for %q: %w", filename, err)
		}
		mediaData, err := os.ReadFile(mediaPath)
		if err != nil {
			// Skip missing media files (Anki does this too)
			continue
		}
		if err := addFileToZip(zipWriter, idxStr, mediaData); err != nil {
			return fmt.Errorf("add media file %s to zip: %w", filename, err)
		}
	}

	// Explicitly close the zip writer to flush the central directory and
	// catch any write errors (e.g., disk full). The deferred close remains as
	// a fallback for early-exit error paths.
	if err := zipWriter.Close(); err != nil {
		return fmt.Errorf("close zip writer: %w", err)
	}

	return nil
}
