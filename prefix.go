package lzham

import (
	"math/bits"
	"sort"
)

// generatePolarCodes ports the alternate prefix coder signalled by the
// stream's configuration bit. The TF2 compressor selects Huffman, but the
// original decompressor also accepts polar-coded input.
func generatePolarCodes(freq []uint16, sizes []uint8) error {
	type symbol struct {
		freq int
		id   int
	}
	symbols := make([]symbol, 0, len(freq))
	total := 0
	for i, f := range freq {
		sizes[i] = 0
		if f != 0 {
			symbols = append(symbols, symbol{int(f), i})
			total += int(f)
		}
	}
	if len(symbols) == 0 {
		return errBadCode
	}
	if len(symbols) == 1 {
		sizes[symbols[0].id] = 1
		return nil
	}
	sort.SliceStable(symbols, func(i, j int) bool { return symbols[i].freq < symbols[j].freq })
	adjusted := make([]int, len(symbols))
	current := 0
	for i := range adjusted {
		adjusted[i] = 1 << (bits.Len(uint(symbols[len(symbols)-1-i].freq)) - 1)
		current += adjusted[i]
	}
	treeTotal := 1 << bits.Len(uint(total-1))
	start := 0
	for current < treeTotal && start < len(symbols) {
		for i := start; i < len(symbols); i++ {
			f := adjusted[i]
			if current+f <= treeTotal {
				adjusted[i] += f
				current += f
				if current == treeTotal {
					break
				}
			} else {
				start = i + 1
			}
		}
	}
	if current != treeTotal {
		return errBadCode
	}
	treeBits := bits.Len(uint(treeTotal))
	for i, f := range adjusted {
		sizes[symbols[len(symbols)-1-i].id] = uint8(treeBits - bits.Len(uint(f)))
	}
	return nil
}

// limitCodeSizes is the LHArc tree-length adjustment used by LZHAM.
func limitCodeSizes(sizes []uint8, maximum int) error {
	var counts, offsets [35]int
	limit := false
	for _, n := range sizes {
		if n >= uint8(len(counts)) {
			return errBadCode
		}
		counts[n]++
		limit = limit || int(n) > maximum
	}
	if !limit {
		return nil
	}
	used := 0
	for i := 1; i < len(counts); i++ {
		offsets[i] = used
		used += counts[i]
	}
	if used < 2 {
		return nil
	}
	if used > 1<<maximum {
		return errBadCode
	}
	for i := maximum + 1; i < len(counts); i++ {
		counts[maximum] += counts[i]
	}
	total := 0
	for i := maximum; i > 0; i-- {
		total += counts[i] << (maximum - i)
	}
	if total == 1<<maximum {
		return nil
	}
	for total != 1<<maximum {
		counts[maximum]--
		i := maximum - 1
		for i > 0 && counts[i] == 0 {
			i--
		}
		if i == 0 {
			return errBadCode
		}
		counts[i]--
		counts[i+1] += 2
		total--
	}
	sorted := make([]uint8, 0, used)
	for i := 1; i <= maximum; i++ {
		for n := 0; n < counts[i]; n++ {
			sorted = append(sorted, uint8(i))
		}
	}
	for i, n := range sizes {
		if n != 0 {
			sizes[i] = sorted[offsets[n]]
			offsets[n]++
		}
	}
	return nil
}
