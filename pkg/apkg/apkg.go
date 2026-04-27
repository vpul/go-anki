// Package apkg provides functionality to import and export Anki decks
// in the .apkg (deck package) and .colpkg (collection package) formats.
//
// An .apkg file is a ZIP archive containing:
//   - collection.anki2: SQLite database with the deck data
//   - media: JSON file mapping media indices to filenames
//   - Numeric media files (0, 1, 2, ...) referenced by the media map
//
// A .colpkg file is a ZIP archive containing:
//   - collection.anki21b: Zstandard-compressed SQLite database
//   - collection.anki2: Placeholder file
//   - media: JSON file mapping media indices to filenames
//   - Numeric media files
package apkg

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/klauspost/compress/zstd"
)

// ExportOptions configures how an .apkg file is created.
type ExportOptions struct {
	// SourceDB is the path to the .anki2 SQLite database file to export.
	SourceDB string

	// OutputPath is where the .apkg file will be written.
	OutputPath string

	// DeckName is the name of the deck being exported (used in metadata).
	DeckName string

	// DeckID is the ID of the deck being exported.
	// If 0, exports all decks.
	DeckID int64

	// MediaDir is the directory containing media files (images, audio, etc.).
	// If empty, no media files are included.
	MediaDir string

	// MediaMap maps media indices to filenames.
	// If nil and MediaDir is set, media files are auto-discovered.
	MediaMap MediaMap
}

// Security limits for ZIP extraction.
const (
	// maxFileSize is the maximum allowed decompressed size for a single file (50MB).
	maxFileSize int64 = 50 * 1024 * 1024

	// maxTotalSize is the maximum allowed total decompressed size across all files (500MB).
	maxTotalSize int64 = 500 * 1024 * 1024

	// maxFileCount is the maximum number of files allowed in a ZIP archive.
	maxFileCount = 10000
)

// ImportResult contains information about an imported .apkg or .colpkg file.
type ImportResult struct {
	// MediaFilesImported is the number of media files extracted.
	MediaFilesImported int
}

// MediaMap represents the media mapping file inside an .apkg.
// Keys are string representations of numeric indices (e.g., "0", "1", "2").
// Values are the original filenames (e.g., "image.png", "audio.mp3").
type MediaMap map[string]string

// ExportApkg creates an .apkg file from an Anki collection database.
//
// The .apkg format is a ZIP archive containing the collection database
// and optionally media files. It can be imported by Anki Desktop, AnkiDroid,
// and AnkiMobile.
func ExportApkg(opts ExportOptions) error {
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

	// Create the .apkg ZIP file
	outFile, err := os.Create(opts.OutputPath)
	if err != nil {
		return fmt.Errorf("create output file: %w", err)
	}
	defer func() { _ = outFile.Close() }()

	zipWriter := zip.NewWriter(outFile)

	// Add collection.anki2
	if err := addFileToZip(zipWriter, "collection.anki2", dbData); err != nil {
		return fmt.Errorf("add collection to zip: %w", err)
	}

	// Add media map
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

	if err := zipWriter.Close(); err != nil {
		return fmt.Errorf("close zip writer: %w", err)
	}
	return nil
}

// ImportApkg extracts an .apkg file, returning the collection database path
// and media directory path.
//
// The extracted database can be opened with collection.Open() for reading.
func ImportApkg(apkgPath string, destDir string) (*ImportResult, error) {
	if apkgPath == "" {
		return nil, fmt.Errorf("apkg path is required")
	}

	// Open the .apkg ZIP file
	reader, err := zip.OpenReader(apkgPath)
	if err != nil {
		return nil, fmt.Errorf("open apkg file: %w", err)
	}
	defer func() { _ = reader.Close() }()

	// Enforce file count limit
	if len(reader.File) > maxFileCount {
		return nil, fmt.Errorf("archive contains %d files, exceeds maximum of %d", len(reader.File), maxFileCount)
	}

	// Ensure destination directory exists
	if err := os.MkdirAll(destDir, 0755); err != nil {
		return nil, fmt.Errorf("create destination directory: %w", err)
	}

	var totalSize int64

	// First pass: extract the collection database and parse the media map.
	var mediaMap MediaMap
	for _, file := range reader.File {
		// Validate entry name for path traversal and null bytes
		if err := validateZipEntryName(file.Name, destDir); err != nil {
			return nil, fmt.Errorf("invalid entry %q: %w", file.Name, err)
		}

		switch file.Name {
		case "collection.anki2", "collection.anki21b":
			dbPath := filepath.Join(destDir, "collection.anki2")
			written, err := extractZipFileWithLimit(file, dbPath, maxFileSize)
			totalSize += written
			if totalSize > maxTotalSize {
				return nil, fmt.Errorf("total decompressed size exceeds %d byte limit", maxTotalSize)
			}
			if err != nil {
				return nil, fmt.Errorf("extract collection: %w", err)
			}
		case "media":
			rc, err := file.Open()
			if err != nil {
				return nil, fmt.Errorf("open media map: %w", err)
			}
			limited := io.LimitReader(rc, maxFileSize)
			data, err := io.ReadAll(limited)
			_ = rc.Close()
			if err != nil {
				return nil, fmt.Errorf("read media map: %w", err)
			}
			totalSize += int64(len(data))
			if totalSize > maxTotalSize {
				return nil, fmt.Errorf("total decompressed size exceeds %d byte limit", maxTotalSize)
			}
			if err := json.Unmarshal(data, &mediaMap); err != nil {
				// Log but don't fail: a malformed media map shouldn't
				// prevent the rest of the deck from importing.
				log.Printf("warning: failed to parse media map: %v", err)
			}
		}
	}

	// Second pass: extract media files using the parsed map.
	result := &ImportResult{}
	if mediaMap != nil {
		mediaDir := filepath.Join(destDir, "collection.media")
		if err := os.MkdirAll(mediaDir, 0755); err != nil {
			return nil, fmt.Errorf("create media directory: %w", err)
		}
		for _, file := range reader.File {
			if !isNumeric(file.Name) {
				continue
			}
			filename, ok := mediaMap[file.Name]
			if !ok {
				continue
			}
			if err := validateMediaFilename(filename); err != nil {
				return nil, fmt.Errorf("invalid media filename in archive: %w", err)
			}
			mediaPath := filepath.Join(mediaDir, filename)
			if err := validatePathWithinDir(mediaPath, mediaDir); err != nil {
				return nil, fmt.Errorf("media file path escapes destination: %s: %w", filename, err)
			}
			written, err := extractZipFileWithLimit(file, mediaPath, maxFileSize)
			totalSize += written
			if totalSize > maxTotalSize {
				return nil, fmt.Errorf("total decompressed size exceeds %d byte limit", maxTotalSize)
			}
			if err != nil {
				return nil, fmt.Errorf("extract media file %s: %w", filename, err)
			}
			result.MediaFilesImported++
		}
	}

	return result, nil
}

// ImportColpkg extracts a .colpkg (collection package) file.
// .colpkg files use Zstandard compression for the database.
func ImportColpkg(colpkgPath string, destDir string) (*ImportResult, error) {
	if colpkgPath == "" {
		return nil, fmt.Errorf("colpkg path is required")
	}

	reader, err := zip.OpenReader(colpkgPath)
	if err != nil {
		return nil, fmt.Errorf("open colpkg file: %w", err)
	}
	defer func() { _ = reader.Close() }()

	// Enforce file count limit
	if len(reader.File) > maxFileCount {
		return nil, fmt.Errorf("archive contains %d files, exceeds maximum of %d", len(reader.File), maxFileCount)
	}

	if err := os.MkdirAll(destDir, 0755); err != nil {
		return nil, fmt.Errorf("create destination directory: %w", err)
	}

	var totalSize int64

	// First pass: extract/decompress the collection database and parse the media map.
	var mediaMap MediaMap
	for _, file := range reader.File {
		// Validate entry name for path traversal and null bytes
		if err := validateZipEntryName(file.Name, destDir); err != nil {
			return nil, fmt.Errorf("invalid entry %q: %w", file.Name, err)
		}

		switch file.Name {
		case "collection.anki21b":
			dbPath := filepath.Join(destDir, "collection.anki2")
			written, err := extractZstdZipFileWithLimit(file, dbPath, maxFileSize)
			totalSize += written
			if totalSize > maxTotalSize {
				return nil, fmt.Errorf("total decompressed size exceeds %d byte limit", maxTotalSize)
			}
			if err != nil {
				return nil, fmt.Errorf("decompress collection: %w", err)
			}
		case "collection.anki2":
			// In .colpkg, this is a small placeholder file — skip it
		case "media":
			rc, err := file.Open()
			if err != nil {
				return nil, fmt.Errorf("open media map: %w", err)
			}
			limited := io.LimitReader(rc, maxFileSize)
			data, err := io.ReadAll(limited)
			_ = rc.Close()
			if err != nil {
				return nil, fmt.Errorf("read media map: %w", err)
			}
			totalSize += int64(len(data))
			if totalSize > maxTotalSize {
				return nil, fmt.Errorf("total decompressed size exceeds %d byte limit", maxTotalSize)
			}
			if err := json.Unmarshal(data, &mediaMap); err != nil {
				// Log but don't fail: a malformed media map shouldn't
				// prevent the rest of the deck from importing.
				log.Printf("warning: failed to parse media map: %v", err)
			}
		}
	}

	// Second pass: extract media files using the parsed map.
	result := &ImportResult{}
	if mediaMap != nil {
		mediaDir := filepath.Join(destDir, "collection.media")
		if err := os.MkdirAll(mediaDir, 0755); err != nil {
			return nil, fmt.Errorf("create media directory: %w", err)
		}
		for _, file := range reader.File {
			if !isNumeric(file.Name) {
				continue
			}
			filename, ok := mediaMap[file.Name]
			if !ok {
				continue
			}
			if err := validateMediaFilename(filename); err != nil {
				return nil, fmt.Errorf("invalid media filename in archive: %w", err)
			}
			mediaPath := filepath.Join(mediaDir, filename)
			if err := validatePathWithinDir(mediaPath, mediaDir); err != nil {
				return nil, fmt.Errorf("media file path escapes destination: %s: %w", filename, err)
			}
			written, err := extractZipFileWithLimit(file, mediaPath, maxFileSize)
			totalSize += written
			if totalSize > maxTotalSize {
				return nil, fmt.Errorf("total decompressed size exceeds %d byte limit", maxTotalSize)
			}
			if err != nil {
				return nil, fmt.Errorf("extract media file %s: %w", filename, err)
			}
			result.MediaFilesImported++
		}
	}

	return result, nil
}

// validatePathWithinDir checks that the resolved path stays within the
// specified directory, preventing path traversal attacks.
func validatePathWithinDir(path, dir string) error {
	cleanPath := filepath.Clean(path)
	cleanDir := filepath.Clean(dir)
	if !strings.HasPrefix(cleanPath+string(os.PathSeparator), cleanDir+string(os.PathSeparator)) && cleanPath != cleanDir {
		return fmt.Errorf("path escapes directory: %s", path)
	}
	return nil
}

// validateZipEntryName validates that a ZIP entry name does not contain null
// bytes and resolves within the destination directory (path traversal check).
func validateZipEntryName(entryName, destDir string) error {
	// Reject null bytes
	if strings.ContainsRune(entryName, 0) {
		return fmt.Errorf("entry name contains null byte")
	}

	// Check for path traversal: resolve the path and verify it stays within destDir
	fullPath := filepath.Join(destDir, entryName)
	rel, err := filepath.Rel(destDir, fullPath)
	if err != nil {
		return fmt.Errorf("path resolution error: %w", err)
	}
	if strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
		return fmt.Errorf("entry resolves outside destination directory")
	}
	return nil
}

// validateMediaFilename checks that a media filename is safe for extraction.
// It must contain only alphanumeric characters, underscores, hyphens, and dots;
// must not start with a dot (no hidden files); must not contain ".."; and must
// not be empty.
var validMediaFilenameRe = regexp.MustCompile(`^[a-zA-Z0-9_]([a-zA-Z0-9_.-]*[a-zA-Z0-9_.-])?$|^[a-zA-Z0-9_]$`)

func validateMediaFilename(name string) error {
	if name == "" {
		return fmt.Errorf("invalid media filename: %q", name)
	}
	if strings.HasPrefix(name, ".") {
		return fmt.Errorf("invalid media filename: %q", name)
	}
	if strings.Contains(name, "..") {
		return fmt.Errorf("invalid media filename: %q", name)
	}
	if !validMediaFilenameRe.MatchString(name) {
		return fmt.Errorf("invalid media filename: %q", name)
	}
	return nil
}

// extractZipFileWithLimit extracts a single file from a ZIP archive to disk,
// enforcing a maximum decompressed size. Returns the number of bytes written.
func extractZipFileWithLimit(file *zip.File, destPath string, maxSize int64) (int64, error) {
	rc, err := file.Open()
	if err != nil {
		return 0, fmt.Errorf("open zip entry %s: %w", file.Name, err)
	}
	defer func() { _ = rc.Close() }()

	// Check uncompressed size upfront if available
	if file.UncompressedSize64 > uint64(maxSize) {
		return 0, fmt.Errorf("file %q decompressed size %d exceeds %d byte limit", file.Name, file.UncompressedSize64, maxSize)
	}

	outFile, err := os.Create(destPath)
	if err != nil {
		return 0, fmt.Errorf("create file %s: %w", destPath, err)
	}
	defer func() { _ = outFile.Close() }()

	limited := io.LimitReader(rc, maxSize+1) // +1 to detect overflow
	var buf bytes.Buffer
	written, err := io.Copy(&buf, limited)
	if err != nil {
		return 0, fmt.Errorf("read zip entry %s: %w", file.Name, err)
	}
	if written > maxSize {
		return 0, fmt.Errorf("file %q decompressed size exceeds %d byte limit", file.Name, maxSize)
	}

	if _, err := outFile.Write(buf.Bytes()); err != nil {
		return 0, fmt.Errorf("write file %s: %w", destPath, err)
	}

	return written, nil
}

// extractZstdZipFileWithLimit extracts a Zstandard-compressed file from a ZIP archive,
// enforcing size limits on both compressed and decompressed data. Returns decompressed size.
// This streams through the zstd decompressor with an output limit, preventing zip bombs
// where a small compressed payload expands to many gigabytes.
func extractZstdZipFileWithLimit(file *zip.File, destPath string, maxSize int64) (int64, error) {
	rc, err := file.Open()
	if err != nil {
		return 0, fmt.Errorf("open zip entry %s: %w", file.Name, err)
	}
	defer func() { _ = rc.Close() }()

	// Read compressed data with limit
	limitedCompressed := io.LimitReader(rc, maxSize+1) // +1 to detect oversized compressed data
	compressed, err := io.ReadAll(limitedCompressed)
	if err != nil {
		return 0, fmt.Errorf("read zip entry %s: %w", file.Name, err)
	}

	// Stream decompression with an output size limit to prevent decompression bombs.
	// A small compressed payload can expand to many gigabytes; this bounds the memory usage.
	zstdReader, err := zstd.NewReader(bytes.NewReader(compressed))
	if err != nil {
		return 0, fmt.Errorf("create zstd reader for %s: %w", file.Name, err)
	}
	defer zstdReader.Close()

	limitedDec := io.LimitReader(zstdReader, maxSize+1) // +1 to detect overflow
	decompressed, err := io.ReadAll(limitedDec)
	if err != nil {
		return 0, fmt.Errorf("decompress %s: %w", file.Name, err)
	}

	if int64(len(decompressed)) > maxSize {
		return 0, fmt.Errorf("file %q decompressed size %d exceeds %d byte limit", file.Name, len(decompressed), maxSize)
	}

	// Write decompressed data
	if err := os.WriteFile(destPath, decompressed, 0644); err != nil {
		return 0, fmt.Errorf("write file %s: %w", destPath, err)
	}

	return int64(len(decompressed)), nil
}

// addFileToZip adds a byte slice as a file to a ZIP writer.
func addFileToZip(w *zip.Writer, name string, data []byte) error {
	f, err := w.Create(name)
	if err != nil {
		return fmt.Errorf("create zip entry %s: %w", name, err)
	}
	if _, err := f.Write(data); err != nil {
		return fmt.Errorf("write zip entry %s: %w", name, err)
	}
	return nil
}

// discoverMediaFiles scans a directory for media files and creates a MediaMap.
// Files are assigned sequential numeric indices.
func discoverMediaFiles(mediaDir string) (MediaMap, error) {
	entries, err := os.ReadDir(mediaDir)
	if err != nil {
		return nil, fmt.Errorf("read media directory: %w", err)
	}

	mediaMap := make(MediaMap)
	idx := 0
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		// Skip hidden files and files with invalid names
		if strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		if err := validateMediaFilename(entry.Name()); err != nil {
			continue
		}
		mediaMap[fmt.Sprintf("%d", idx)] = entry.Name()
		idx++
	}

	return mediaMap, nil
}

// isNumeric returns true if a string is composed entirely of digits.
// Used to identify media file entries in .apkg archives.
func isNumeric(s string) bool {
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return len(s) > 0
}
