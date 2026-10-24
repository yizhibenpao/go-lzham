package lzham

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestReferenceDecoderBranches(t *testing.T) {
	type reference struct {
		File       string
		Size       int
		SHA256     string
		Adler, CRC uint32
	}
	var references []reference
	readReferenceJSON(t, "testdata/decode.json", &references)
	for _, tc := range references {
		t.Run(tc.File, func(t *testing.T) {
			src, err := os.ReadFile(filepath.Join("testdata", tc.File))
			if err != nil {
				t.Fatal(err)
			}
			got := make([]byte, tc.Size)
			n, a, c, err := Decompress(got, src)
			if err != nil || n != tc.Size || a != tc.Adler || c != tc.CRC || fmt.Sprintf("%x", sha256.Sum256(got)) != tc.SHA256 {
				t.Fatalf("size=%d adler=%08x crc=%08x err=%v", n, a, c, err)
			}
		})
	}
}

func TestReferenceDecoderErrors(t *testing.T) {
	type errorCase struct {
		Name, Source, Error string
		Capacity, N         int
		Adler, CRC          uint32
	}
	var cases []errorCase
	readReferenceJSON(t, "testdata/errors.json", &cases)
	failed := 0
	for _, tc := range cases {
		src, err := hex.DecodeString(tc.Source)
		if err != nil {
			t.Fatal(err)
		}
		n, a, c, err := Decompress(make([]byte, tc.Capacity), src)
		message := ""
		if err != nil {
			message = err.Error()[7:]
		}
		if n != tc.N || a != tc.Adler || c != tc.CRC || message != tc.Error {
			failed++
			if failed <= 20 {
				t.Logf("%s: got (%d,%08x,%08x,%q) want (%d,%08x,%08x,%q)", tc.Name, n, a, c, message, tc.N, tc.Adler, tc.CRC, tc.Error)
			}
		}
	}
	if failed != 0 {
		t.Fatalf("%d/%d differences", failed, len(cases))
	}
}
