package lzham

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"hash/crc32"
	"math/rand"
	"testing"
)

func TestPureGoRoundTrip(t *testing.T) {
	inputs := [][]byte{
		[]byte("tf2lzham"),
		bytes.Repeat([]byte{0}, 200000),
		bytes.Repeat([]byte("abcdef0123456789"), 20000),
	}
	random := make([]byte, 300000)
	if _, err := rand.New(rand.NewSource(1)).Read(random); err != nil {
		t.Fatal(err)
	}
	inputs = append(inputs, random)

	for _, want := range inputs {
		compressed := make([]byte, len(want)*2+1024)
		n, wantAdler, wantCRC, err := Compress(compressed, want)
		if err != nil {
			t.Fatal(err)
		}
		compressed = compressed[:n]

		output := make([]byte, len(want))
		gotN, gotAdler, gotCRC, err := Decompress(output, compressed)
		if err != nil {
			t.Fatalf("input length %d compressed length %d: %v", len(want), len(compressed), err)
		}
		if gotN != len(want) || !bytes.Equal(output[:gotN], want) {
			t.Fatalf("round trip mismatch: got %d bytes, want %d", gotN, len(want))
		}
		wantDecodedCRC := crc32.ChecksumIEEE(want)
		if gotAdler != wantAdler || gotCRC != wantDecodedCRC {
			t.Fatalf("checksums = %08x/%08x, want %08x/%08x (compress returned %08x)", gotAdler, gotCRC, wantAdler, wantDecodedCRC, wantCRC)
		}
	}
}

func TestPureGoRejectsInvalidInput(t *testing.T) {
	if _, _, _, err := Decompress(make([]byte, 32), []byte{0}); err == nil {
		t.Fatal("invalid input was accepted")
	}
}

func TestPureGoDecodesCxxCompressedStream(t *testing.T) {
	compressed, err := hex.DecodeString("11e5cfd001a195b00c80035c11c600c0a3100ae53efe1af8")
	if err != nil {
		t.Fatal(err)
	}
	want := []byte("hello hello hello hello hello")
	got := make([]byte, len(want))
	n, adler, crc, err := Decompress(got, compressed)
	if err != nil {
		t.Fatal(err)
	}
	if n != len(want) || !bytes.Equal(got[:n], want) {
		t.Fatalf("decoded %d bytes %q, want %q", n, got[:n], want)
	}
	if adler != 0xa3100ae5 || crc != 0x3853d2f6 {
		t.Fatalf("checksums = %08x/%08x", adler, crc)
	}
}

func TestPureGoDecodesCxxLongMatchStream(t *testing.T) {
	compressed, err := base64.StdEncoding.DecodeString("EAALS5GFiY2RlZmdoaQtqa2xtbm9wYYLmhA/Vz9/YX95tz9/f2cAA9UAADKAAAAAAAAAAOPuAMYAwNoGNRcacPA4")
	if err != nil {
		t.Fatal(err)
	}
	want := bytes.Repeat([]byte("abcdefghijklmnop"), 400)
	got := make([]byte, len(want))
	n, adler, crc, err := Decompress(got, compressed)
	if err != nil {
		t.Fatal(err)
	}
	if n != len(want) || !bytes.Equal(got[:n], want) {
		t.Fatalf("decoded %d bytes, want %d", n, len(want))
	}
	if adler != 0xda063517 || crc != crc32.ChecksumIEEE(want) {
		t.Fatalf("checksums = %08x/%08x", adler, crc)
	}
}

func TestBufferContracts(t *testing.T) {
	src := []byte("hello hello hello hello hello")
	want, err := hex.DecodeString("11e5cfd001a195b00c80035c11c600c0a3100ae53efe1af8")
	if err != nil {
		t.Fatal(err)
	}
	for capacity := 1; capacity < len(want); capacity++ {
		dst := bytes.Repeat([]byte{0xa5}, capacity)
		n, a, c, err := Compress(dst, src)
		if err == nil || err.Error() != errOutputTooSmall.Error() || n != 0 || a != 0 || c != 0 ||
			!bytes.Equal(dst, bytes.Repeat([]byte{0xa5}, capacity)) {
			t.Fatalf("short compression buffer %d: %d %08x %08x %v", capacity, n, a, c, err)
		}
	}
	for _, offsets := range [][2]int{{0, 0}, {0, 4}, {4, 0}} {
		buf := make([]byte, 128)
		copy(buf[offsets[0]:], src)
		dst := buf[offsets[1]:]
		n, _, _, err := Compress(dst, buf[offsets[0]:offsets[0]+len(src)])
		if err != nil || n != len(want) || !bytes.Equal(dst[:n], want) {
			t.Fatalf("overlapping compression buffers %v: %v", offsets, err)
		}
	}
	for _, fn := range []func([]byte, []byte) (int, uint32, uint32, error){Compress, Decompress} {
		for _, buffers := range [][2][]byte{{nil, src}, {src, nil}, {nil, nil}} {
			n, a, c, err := fn(buffers[0], buffers[1])
			if err == nil || err.Error() != errZeroLength.Error() || n != 0 || a != 0 || c != 0 {
				t.Fatalf("empty buffer: %d %08x %08x %v", n, a, c, err)
			}
		}
	}
}

func TestErrorInstancesMatchWrapper(t *testing.T) {
	_, _, _, first := Compress(nil, nil)
	_, _, _, second := Compress(nil, nil)
	if first == second || first.Error() != second.Error() {
		t.Fatal("the original wrapper returns a fresh error on each call")
	}
}

func TestBitReaderLargeOffset(t *testing.T) {
	r := bitReader{src: []byte{0xff}, pos: 1 << 32}
	value, err := r.get(8)
	if err != nil || value != 0 || r.pos != (1<<32)+8 {
		t.Fatal("bit offsets must not wrap on 32-bit builds")
	}
}

func TestIndependentCalls(t *testing.T) {
	for i := 0; i < 8; i++ {
		t.Run(string(rune('a'+i)), func(t *testing.T) {
			t.Parallel()
			src := bytes.Repeat([]byte("independent LZHAM state"), 256)
			dst := make([]byte, len(src)*2)
			n, _, _, err := Compress(dst, src)
			if err != nil {
				t.Fatal(err)
			}
			got := make([]byte, len(src))
			m, _, _, err := Decompress(got, dst[:n])
			if err != nil || m != len(src) || !bytes.Equal(got, src) {
				t.Fatalf("independent call: %v", err)
			}
		})
	}
}

func FuzzDecompress(f *testing.F) {
	f.Add([]byte{0})
	f.Add([]byte{0xff})
	f.Add([]byte{})
	src, err := hex.DecodeString("11e5cfd001a195b00c80035c11c600c0a3100ae53efe1af8")
	if err != nil {
		f.Fatal(err)
	}
	f.Add(src)
	f.Fuzz(func(t *testing.T, src []byte) {
		if len(src) > 256*1024 {
			t.Skip()
		}
		dst := make([]byte, 65536)
		n, a, c, err := Decompress(dst, src)
		if err != nil {
			if n != 0 || a != 0 || c != 0 {
				t.Fatal("error returned non-zero results")
			}
			return
		}
		if n < 0 || n > len(dst) || crc32.ChecksumIEEE(dst[:n]) != c {
			t.Fatal("invalid decompression result")
		}
	})
}
