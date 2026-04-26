package apkg

import (
	"bytes"
	"github.com/klauspost/compress/zstd"
	"io"
)

// decompressZstd decompresses Zstandard-compressed data.
// Used for .colpkg files where collection.anki21b is zstd-compressed.
func decompressZstd(data []byte) ([]byte, error) {
	reader, err := zstd.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	defer reader.Close()

	decompressed, err := io.ReadAll(reader)
	if err != nil {
		return nil, err
	}

	return decompressed, nil
}

// compressZstd compresses data using Zstandard.
// Used for creating .colpkg files.
func compressZstd(data []byte) ([]byte, error) {
	var buf bytes.Buffer
	writer, err := zstd.NewWriter(&buf)
	if err != nil {
		return nil, err
	}
	if _, err := writer.Write(data); err != nil {
		writer.Close()
		return nil, err
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}

	return buf.Bytes(), nil
}