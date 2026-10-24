package lzham

import (
	"bytes"
	"fmt"
	"hash/adler32"
	"hash/crc32"
	"os"
	"path/filepath"
	"testing"
)

// Exercise the symbol formatter independently of the fixed UBER parser,
// including huge matches that this parser does not normally emit. The plan is
// the one used by the original-C++ testdata/oracle/fixture_generator.cpp.
func TestReferenceSymbolEncoder(t *testing.T) {
	var source []byte
	var plans [3][]lzDecision
	var starts [3]int
	rng := uint32(12345)
	for block := range plans {
		starts[block] = len(source)
		history := [4]int{1, 1, 1, 1}
		literal := func(c byte) {
			plans[block] = append(plans[block], lzDecision{position: len(source) - starts[block]})
			source = append(source, c)
		}
		match := func(n, distance int) {
			actual := distance
			rep := 3
			if distance < 0 {
				rep = -distance - 1
				actual = history[rep]
			}
			plans[block] = append(plans[block], lzDecision{len(source) - starts[block], n, distance})
			for i := 0; i < n; i++ {
				source = append(source, source[len(source)-actual])
			}
			for i := rep; i > 0; i-- {
				history[i] = history[i-1]
			}
			history[0] = actual
		}
		for i := 0; i < 48192; i++ {
			rng ^= rng << 13
			rng ^= rng >> 17
			rng ^= rng << 5
			mask := uint32(255)
			if i >= 8192 {
				mask = 7
			}
			literal(byte(rng & mask))
		}
		for _, distance := range []int{1, 2, 3, 4, 7, 8, 15, 16, 31, 32, 63, 64, 127, 128, 255, 256, 511, 512, 1023, 1024, 2047, 2048, 4095, 4096, 8191} {
			match(2, distance)
			match(3, -1)
			match(4, -2)
			match(5, -3)
			match(6, -4)
			literal(37)
			match(1, -1)
		}
		for _, length := range []int{9, 256, 257, 258, 513, 514, 1537, 1538, 5633, 5634, 65536} {
			match(length, 1)
			match(length, -1)
		}
		plans[block] = append(plans[block], lzDecision{position: len(source) - starts[block], length: -1})
		for i := 0; i < 128; i++ {
			literal(byte(i))
		}
	}
	for config := 0; config < 4; config++ {
		t.Run(fmt.Sprint(config), func(t *testing.T) {
			models := newDecoder(nil, nil)
			if err := models.initModels(config&2 != 0, config&1 != 0); err != nil {
				t.Fatal(err)
			}
			finder := &matchFinder{dict: make([]byte, dictSize+65536)}
			copy(finder.dict, source)
			var output []byte
			decoded := append([]byte(nil), source...)
			encodeBlock := func(plan []lzDecision, flush int, first bool) {
				t.Helper()
				encoder := symbolEncoder{}
				encoder.start()
				if first {
					encoder.putBits(uint32(config), 2)
				}
				encoder.putBits(blockComp, 2)
				encoder.arithInit()
				encoder.putBits(uint32(flush), 2)
				switch flush {
				case 1:
					models.resetUpdateRates()
				case 2:
					if err := models.resetModels(); err != nil {
						t.Fatal(err)
					}
				}
				state := initialParseState()
				end := 0
				for _, decision := range plan {
					if decision.length == -1 {
						context := matchContext(finder, decision.position, state.state)
						encoder.arithBit(1, &models.isMatch[context])
						encoder.arithBit(0, &models.isRep[state.state])
						if err := models.main.encode(&encoder, 1); err != nil {
							t.Fatal(err)
						}
						state = initialParseState()
					} else {
						if err := encodeDecision(&encoder, models, finder, &state, decision); err != nil {
							t.Fatal(err)
						}
						end = decision.position + maxInt(1, decision.length)
					}
				}
				context := matchContext(finder, end, state.state)
				encoder.arithBit(1, &models.isMatch[context])
				encoder.arithBit(0, &models.isRep[state.state])
				if err := models.main.encode(&encoder, 0); err != nil {
					t.Fatal(err)
				}
				encoder.stop()
				output = append(output, encoder.assemble()...)
			}
			for block := range plans {
				finder.position = uint32(starts[block])
				encodeBlock(plans[block], block, block == 0)
			}
			for i := range finder.dict {
				finder.dict[i] = 'X'
			}
			finder.position = 0
			pos := 1
			plan := []lzDecision{{}}
			for i := 0; i < 16; i++ {
				length := 65536
				if i == 15 {
					length--
				}
				plan = append(plan, lzDecision{pos, length, -1})
				pos += length
			}
			encodeBlock(plan, 2, false)
			decoded = append(decoded, bytes.Repeat([]byte{'X'}, pos)...)
			pos, plan = 0, nil
			for slot := uint(1); slot < models.numSlots; slot++ {
				lo := int(models.slotBase[slot])
				hi := lo + (1 << models.slotExtra[slot]) - 1
				for _, pair := range [][2]int{{2, lo}, {9, hi}, {257, lo}} {
					plan = append(plan, lzDecision{pos, pair[0], pair[1]})
					pos += pair[0]
				}
			}
			encodeBlock(plan, 2, false)
			decoded = append(decoded, bytes.Repeat([]byte{'X'}, pos)...)
			tail := dynamicBitWriter{buf: output, pos: uint(len(output)) * 8}
			tail.put(blockEOF, 2)
			tail.alignByte()
			tail.put(adler32.Checksum(decoded), 32)
			tail.put(crc32.ChecksumIEEE(decoded), 32)
			want, err := os.ReadFile(filepath.Join("testdata", fmt.Sprintf("config-%d.lzham", config)))
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(tail.buf, want) {
				t.Fatalf("native formatter differs: Go %d bytes, C++ %d bytes", len(tail.buf), len(want))
			}
		})
	}
}
