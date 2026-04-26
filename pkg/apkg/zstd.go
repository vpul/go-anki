package apkg

import (
	"bytes"
	"io"

	"github.com/klauspost/compress/zstd"
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
