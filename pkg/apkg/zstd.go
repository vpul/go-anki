package apkg

import (
	"bytes"
	"fmt"

	"github.com/klauspost/compress/zstd"
)

// compressZstd compresses data using Zstandard at the default compression level.
func compressZstd(data []byte) ([]byte, error) {
	var buf bytes.Buffer
	writer, err := zstd.NewWriter(&buf)
	if err != nil {
		return nil, fmt.Errorf("create zstd writer: %w", err)
	}
	if _, err := writer.Write(data); err != nil {
		_ = writer.Close()
		return nil, fmt.Errorf("write zstd data: %w", err)
	}
	if err := writer.Close(); err != nil {
		return nil, fmt.Errorf("close zstd writer: %w", err)
	}
	return buf.Bytes(), nil
}


