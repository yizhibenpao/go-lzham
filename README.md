# go-lzham

Standalone pure-Go library ported from the Titanfall 2 LZHAM memory API in
`github.com/pg9182/tf2lzham`. No cgo, native library, C/C++ compiler,
WebAssembly runtime, or third-party Go dependency is required.

This is an independent Go module, `github.com/yizhibenpao/go-lzham`, with package
name `lzham`. It does not modify or replace the upstream repository.

## Install

```sh
go get github.com/yizhibenpao/go-lzham
```

## API

```go
func Compress(dst, src []byte) (n int, adler32, crc32 uint32, err error)
func Decompress(dst, src []byte) (n int, adler32, crc32 uint32, err error)
```

The caller allocates both buffers. Decompression requires enough space for the
uncompressed content; the stream does not provide an output-size query.

```go
import "github.com/yizhibenpao/go-lzham"
```

Call `lzham.Compress` / `lzham.Decompress`. `example_test.go` contains a runnable
round-trip example.

## Compatibility

Compression retains the original 20-bit dictionary, UBER deterministic optimal
parser, binary-tree match finder, adaptive Huffman and arithmetic coding, block
selection, and checksum behavior. The decoder also supports fast table updates,
polar coding, model resets, all four repeat distances, and huge matches.

The scope matches the two exported Go functions of the original wrapper; it
does not introduce the native C++ streaming, zlib, seed-dictionary, or configurable
compression-level APIs that the wrapper never exposed.

Compatibility includes upstream quirks: compression's CRC is initialized from
the cumulative Adler checksum for each block; decompression returns the normal
IEEE CRC and ignores the trailing stored CRC. Empty buffers are errors. A full
sync flush returns the original one-shot API's incomplete error.

## Tests

Run from this folder (PowerShell):

```powershell
$env:CGO_ENABLED = "0"
go test -count=1 ./...
go vet ./...
go test -run '^$' -fuzz '^FuzzDecompress$' -fuzztime=30s -parallel=2
```

Tests freeze the original C++ results: 125 byte-identical compression cases,
four targeted decoder fixtures, and 770 input/error boundary cases. See
`testdata/oracle/README.md` for provenance and the optional native test oracle.
The files under `testdata/oracle` are test tools, never linked into the library.
