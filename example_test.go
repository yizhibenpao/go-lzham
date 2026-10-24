package lzham_test

import (
	"fmt"

	"github.com/yizhibenpao/go-lzham"
)

func Example() {
	src := []byte("hello, LZHAM")
	compressed := make([]byte, 4096)
	n, _, _, err := lzham.Compress(compressed, src)
	if err != nil {
		panic(err)
	}
	decoded := make([]byte, len(src))
	n, _, _, err = lzham.Decompress(decoded, compressed[:n])
	if err != nil {
		panic(err)
	}
	fmt.Println(string(decoded[:n]))
	// Output: hello, LZHAM
}
