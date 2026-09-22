package ingest

import (
	"fmt"
	"io"
	"strings"

	"github.com/andybalholm/brotli"
	"github.com/klauspost/compress/gzip"
	"github.com/klauspost/compress/zlib"
	"github.com/klauspost/compress/zstd"
)

// decompress оборачивает тело по Content-Encoding. SDK сжимают по-разному:
// Python и PHP — gzip (Python с пакетом brotli — br), Node — gzip для
// больших тел, браузерный SDK обычно не сжимает.
func decompress(encoding string, r io.Reader) (io.ReadCloser, error) {
	switch strings.ToLower(strings.TrimSpace(encoding)) {
	case "", "identity":
		return io.NopCloser(r), nil
	case "gzip", "x-gzip":
		zr, err := gzip.NewReader(r)
		if err != nil {
			return nil, fmt.Errorf("gzip: %w", err)
		}
		return zr, nil
	case "deflate":
		// В HTTP «deflate» по стандарту означает zlib-обёртку.
		zr, err := zlib.NewReader(r)
		if err != nil {
			return nil, fmt.Errorf("deflate: %w", err)
		}
		return zr, nil
	case "br":
		return io.NopCloser(brotli.NewReader(r)), nil
	case "zstd":
		zr, err := zstd.NewReader(r, zstd.WithDecoderMaxMemory(64<<20))
		if err != nil {
			return nil, fmt.Errorf("zstd: %w", err)
		}
		return zr.IOReadCloser(), nil
	default:
		return nil, fmt.Errorf("неподдерживаемый Content-Encoding %q", encoding)
	}
}
