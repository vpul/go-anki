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
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
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

// ImportResult contains information about an imported .apkg or .colpkg file.
type ImportResult struct {
	// CardsImported is the number of cards imported.
	CardsImported int

	// NotesImported is the number of notes imported.
	NotesImported int

	// DecksImported is the list of deck names imported.
	DecksImported []string

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
	defer func() { _ = zipWriter.Close() }()

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
		if opts.MediaDir == "" {
			continue
		}
		mediaPath := filepath.Join(opts.MediaDir, filename)
		mediaData, err := os.ReadFile(mediaPath)
		if err != nil {
			// Skip missing media files (Anki does this too)
			continue
		}
		if err := addFileToZip(zipWriter, idxStr, mediaData); err != nil {
			return fmt.Errorf("add media file %s to zip: %w", filename, err)
		}
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

	// Ensure destination directory exists
	if err := os.MkdirAll(destDir, 0755); err != nil {
		return nil, fmt.Errorf("create destination directory: %w", err)
	}

	result := &ImportResult{}
	var mediaMap MediaMap

	for _, file := range reader.File {
		// Extract collection.anki2
		if file.Name == "collection.anki2" || file.Name == "collection.anki21b" {
			dbPath := filepath.Join(destDir, "collection.anki2")
			if err := extractZipFile(file, dbPath); err != nil {
				return nil, fmt.Errorf("extract collection: %w", err)
			}
			continue
		}

		// Parse media map
		if file.Name == "media" {
			rc, err := file.Open()
			if err != nil {
				return nil, fmt.Errorf("open media map: %w", err)
			}
			data, err := io.ReadAll(rc)
			_ = rc.Close()
			if err != nil {
				return nil, fmt.Errorf("read media map: %w", err)
			}
			if err := json.Unmarshal(data, &mediaMap); err != nil {
				// Not a JSON media map, skip
				continue
			}
			continue
		}

		// Extract media files
		if mediaMap != nil && isNumeric(file.Name) {
			filename, ok := mediaMap[file.Name]
			if !ok {
				// Unknown media file, skip
				continue
			}
			mediaDir := filepath.Join(destDir, "collection.media")
			if err := os.MkdirAll(mediaDir, 0755); err != nil {
				return nil, fmt.Errorf("create media directory: %w", err)
			}
			mediaPath := filepath.Join(mediaDir, filename)
			if err := extractZipFile(file, mediaPath); err != nil {
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

	if err := os.MkdirAll(destDir, 0755); err != nil {
		return nil, fmt.Errorf("create destination directory: %w", err)
	}

	result := &ImportResult{}
	var mediaMap MediaMap

	for _, file := range reader.File {
		// Extract collection.anki21b (zstd-compressed) or collection.anki2
		if file.Name == "collection.anki21b" {
			// Zstandard-compressed — decompress to collection.anki2
			dbPath := filepath.Join(destDir, "collection.anki2")
			if err := extractZstdZipFile(file, dbPath); err != nil {
				return nil, fmt.Errorf("decompress collection: %w", err)
			}
			continue
		}

		if file.Name == "collection.anki2" {
			// In .colpkg, this is a small placeholder file — skip it
			// The real data is in collection.anki21b
			continue
		}

		// Parse media map
		if file.Name == "media" {
			rc, err := file.Open()
			if err != nil {
				return nil, fmt.Errorf("open media map: %w", err)
			}
			data, err := io.ReadAll(rc)
			_ = rc.Close()
			if err != nil {
				return nil, fmt.Errorf("read media map: %w", err)
			}
			if err := json.Unmarshal(data, &mediaMap); err != nil {
				continue
			}
			continue
		}

		// Extract media files
		if mediaMap != nil && isNumeric(file.Name) {
			filename, ok := mediaMap[file.Name]
			if !ok {
				continue
			}
			mediaDir := filepath.Join(destDir, "collection.media")
			if err := os.MkdirAll(mediaDir, 0755); err != nil {
				return nil, fmt.Errorf("create media directory: %w", err)
			}
			mediaPath := filepath.Join(mediaDir, filename)
			if err := extractZipFile(file, mediaPath); err != nil {
				return nil, fmt.Errorf("extract media file %s: %w", filename, err)
			}
			result.MediaFilesImported++
		}
	}

	return result, nil
}

// extractZipFile extracts a single file from a ZIP archive to disk.
func extractZipFile(file *zip.File, destPath string) error {
	rc, err := file.Open()
	if err != nil {
		return fmt.Errorf("open zip entry %s: %w", file.Name, err)
	}
	defer func() { _ = rc.Close() }()

	outFile, err := os.Create(destPath)
	if err != nil {
		return fmt.Errorf("create file %s: %w", destPath, err)
	}
	defer func() { _ = outFile.Close() }()

	if _, err := io.Copy(outFile, rc); err != nil {
		return fmt.Errorf("write file %s: %w", destPath, err)
	}

	return nil
}

// extractZstdZipFile extracts a Zstandard-compressed file from a ZIP archive
// and decompresses it to disk.
func extractZstdZipFile(file *zip.File, destPath string) error {
	rc, err := file.Open()
	if err != nil {
		return fmt.Errorf("open zip entry %s: %w", file.Name, err)
	}
	defer func() { _ = rc.Close() }()

	// Read compressed data
	compressed, err := io.ReadAll(rc)
	if err != nil {
		return fmt.Errorf("read zip entry %s: %w", file.Name, err)
	}

	// Decompress using Zstandard
	decompressed, err := decompressZstd(compressed)
	if err != nil {
		return fmt.Errorf("decompress %s: %w", file.Name, err)
	}

	// Write decompressed data
	if err := os.WriteFile(destPath, decompressed, 0644); err != nil {
		return fmt.Errorf("write file %s: %w", destPath, err)
	}

	return nil
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
		// Skip hidden files and non-media files
		if strings.HasPrefix(entry.Name(), ".") {
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