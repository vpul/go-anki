package apkg

import (
	"bytes"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestExportAndImportApkg(t *testing.T) {
	tmpDir := t.TempDir()
	sourceDB := filepath.Join(tmpDir, "collection.anki2")

	// Create a minimal Anki database
	dbData := createMinimalAnkiDB(t, sourceDB)

	// Create media directory with a test file
	mediaDir := filepath.Join(tmpDir, "media")
	if err := os.MkdirAll(mediaDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mediaDir, "test.png"), []byte("fake-png-data"), 0644); err != nil {
		t.Fatal(err)
	}

	// Export
	apkgPath := filepath.Join(tmpDir, "test.apkg")
	err := ExportApkg(ExportOptions{
		SourceDB:   sourceDB,
		OutputPath: apkgPath,
		DeckName:   "Test Deck",
		MediaDir:   mediaDir,
	})
	if err != nil {
		t.Fatalf("ExportApkg: %v", err)
	}

	// Verify the .apkg file exists
	if _, err := os.Stat(apkgPath); os.IsNotExist(err) {
		t.Fatal("exported .apkg file does not exist")
	}

	// Import
	destDir := filepath.Join(tmpDir, "imported")
	result, err := ImportApkg(apkgPath, destDir)
	if err != nil {
		t.Fatalf("ImportApkg: %v", err)
	}

	// Verify imported database exists
	importedDB := filepath.Join(destDir, "collection.anki2")
	if _, err := os.Stat(importedDB); os.IsNotExist(err) {
		t.Fatal("imported database does not exist")
	}

	// Verify the imported database matches the original
	importedData, err := os.ReadFile(importedDB)
	if err != nil {
		t.Fatalf("read imported database: %v", err)
	}
	if !bytes.Equal(dbData, importedData) {
		t.Error("imported database does not match original")
	}

	if result.MediaFilesImported != 1 {
		t.Errorf("expected 1 media file, got %d", result.MediaFilesImported)
	}
}

func TestExportApkgWithoutMedia(t *testing.T) {
	tmpDir := t.TempDir()
	sourceDB := filepath.Join(tmpDir, "collection.anki2")
	createMinimalAnkiDB(t, sourceDB)

	apkgPath := filepath.Join(tmpDir, "nomedia.apkg")
	err := ExportApkg(ExportOptions{
		SourceDB:   sourceDB,
		OutputPath: apkgPath,
		DeckName:   "No Media Deck",
	})
	if err != nil {
		t.Fatalf("ExportApkg (no media): %v", err)
	}

	destDir := filepath.Join(tmpDir, "imported")
	result, err := ImportApkg(apkgPath, destDir)
	if err != nil {
		t.Fatalf("ImportApkg (no media): %v", err)
	}

	if result.MediaFilesImported != 0 {
		t.Errorf("expected 0 media files, got %d", result.MediaFilesImported)
	}
}

func TestExportApkgMediaMapWithoutMediaDir(t *testing.T) {
	tmpDir := t.TempDir()
	sourceDB := filepath.Join(tmpDir, "collection.anki2")
	createMinimalAnkiDB(t, sourceDB)

	apkgPath := filepath.Join(tmpDir, "test.apkg")
	err := ExportApkg(ExportOptions{
		SourceDB:   sourceDB,
		OutputPath: apkgPath,
		DeckName:   "Test Deck",
		MediaMap:   MediaMap{"0": "test.png"},
	})
	if err == nil {
		t.Fatal("expected error when MediaMap is set but MediaDir is empty")
	}
}

func TestIsNumeric(t *testing.T) {
	tests := []struct {
		input    string
		expected bool
	}{
		{"0", true},
		{"123", true},
		{"", false},
		{"abc", false},
		{"1a2", false},
	}

	for _, tt := range tests {
		result := isNumeric(tt.input)
		if result != tt.expected {
			t.Errorf("isNumeric(%q) = %v, want %v", tt.input, result, tt.expected)
		}
	}
}

func TestDiscoverMediaFiles(t *testing.T) {
	tmpDir := t.TempDir()

	_ = os.WriteFile(filepath.Join(tmpDir, "image.png"), []byte("png"), 0644)
	_ = os.WriteFile(filepath.Join(tmpDir, "audio.mp3"), []byte("mp3"), 0644)
	_ = os.WriteFile(filepath.Join(tmpDir, ".hidden"), []byte("hidden"), 0644)

	mediaMap, err := discoverMediaFiles(tmpDir)
	if err != nil {
		t.Fatalf("discoverMediaFiles: %v", err)
	}

	if len(mediaMap) != 2 {
		t.Errorf("expected 2 media files, got %d", len(mediaMap))
	}

	for _, name := range mediaMap {
		if name == ".hidden" {
			t.Error("hidden file should be skipped")
		}
	}
}

func TestPathTraversalInMediaFilename(t *testing.T) {
	err := validatePathWithinDir("/tmp/media/../etc/passwd", "/tmp/media")
	if err == nil {
		t.Error("expected path traversal to be detected")
	}
}

// createMinimalAnkiDB creates a minimal valid Anki database for testing.
func createMinimalAnkiDB(t *testing.T, path string) []byte {
	t.Helper()

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS col (
		id INTEGER PRIMARY KEY,
		crt INTEGER NOT NULL,
		mod INTEGER NOT NULL,
		ver INTEGER NOT NULL,
		dty INTEGER NOT NULL,
		usn INTEGER NOT NULL,
		lscu INTEGER NOT NULL,
		conf TEXT NOT NULL,
		models TEXT NOT NULL,
		decks TEXT NOT NULL,
		dconf TEXT NOT NULL,
		tags TEXT NOT NULL
	)`)
	if err != nil {
		t.Fatal(err)
	}

	_, err = db.Exec(`INSERT INTO col (id, crt, mod, ver, dty, usn, lscu, conf, models, decks, dconf, tags)
		VALUES (1, 0, 0, 11, 0, 0, 0, '{}', '{}', '{}', '{}', '{}')`)
	if err != nil {
		t.Fatal(err)
	}

	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS notes (
		id INTEGER PRIMARY KEY, guid TEXT NOT NULL, mid INTEGER NOT NULL,
		mod INTEGER NOT NULL, usn INTEGER NOT NULL, tags TEXT NOT NULL,
		flds TEXT NOT NULL, sfld TEXT NOT NULL, csum INTEGER NOT NULL,
		ntype INTEGER NOT NULL, flags INTEGER NOT NULL, data TEXT NOT NULL
	)`)
	if err != nil {
		t.Fatal(err)
	}

	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS cards (
		id INTEGER PRIMARY KEY, nid INTEGER NOT NULL, did INTEGER NOT NULL,
		ord INTEGER NOT NULL, mod INTEGER NOT NULL, usn INTEGER NOT NULL,
		type INTEGER NOT NULL, queue INTEGER NOT NULL, due INTEGER NOT NULL,
		ivl INTEGER NOT NULL, factor INTEGER NOT NULL, reps INTEGER NOT NULL,
		lapses INTEGER NOT NULL, left INTEGER NOT NULL, odue INTEGER NOT NULL,
		odid INTEGER NOT NULL, flags INTEGER NOT NULL, data TEXT NOT NULL
	)`)
	if err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	return data
}
