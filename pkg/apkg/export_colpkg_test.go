package apkg

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestExportAndImportColpkg(t *testing.T) {
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

	// Export as .colpkg
	colpkgPath := filepath.Join(tmpDir, "test.colpkg")
	err := ExportColpkg(ExportColpkgOptions{
		SourceDB:   sourceDB,
		OutputPath: colpkgPath,
		MediaDir:   mediaDir,
	})
	if err != nil {
		t.Fatalf("ExportColpkg: %v", err)
	}

	// Verify the .colpkg file exists
	if _, err := os.Stat(colpkgPath); os.IsNotExist(err) {
		t.Fatal("exported .colpkg file does not exist")
	}

	// Import the .colpkg
	destDir := filepath.Join(tmpDir, "imported")
	result, err := ImportColpkg(colpkgPath, destDir)
	if err != nil {
		t.Fatalf("ImportColpkg: %v", err)
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

func TestExportColpkgWithoutMedia(t *testing.T) {
	tmpDir := t.TempDir()
	sourceDB := filepath.Join(tmpDir, "collection.anki2")
	createMinimalAnkiDB(t, sourceDB)

	colpkgPath := filepath.Join(tmpDir, "nomedia.colpkg")
	err := ExportColpkg(ExportColpkgOptions{
		SourceDB:   sourceDB,
		OutputPath: colpkgPath,
	})
	if err != nil {
		t.Fatalf("ExportColpkg (no media): %v", err)
	}

	destDir := filepath.Join(tmpDir, "imported")
	result, err := ImportColpkg(colpkgPath, destDir)
	if err != nil {
		t.Fatalf("ImportColpkg (no media): %v", err)
	}

	if result.MediaFilesImported != 0 {
		t.Errorf("expected 0 media files, got %d", result.MediaFilesImported)
	}
}

func TestExportColpkgValidation(t *testing.T) {
	err := ExportColpkg(ExportColpkgOptions{})
	if err == nil {
		t.Fatal("expected error with empty SourceDB")
	}

	err = ExportColpkg(ExportColpkgOptions{SourceDB: "/tmp/test.db"})
	if err == nil {
		t.Fatal("expected error with empty OutputPath")
	}

	err = ExportColpkg(ExportColpkgOptions{
		SourceDB:   "/tmp/test.db",
		OutputPath: "/tmp/test.colpkg",
		MediaMap:   MediaMap{"0": "test.png"},
	})
	if err == nil {
		t.Fatal("expected error when MediaMap is set but MediaDir is empty")
	}
}
