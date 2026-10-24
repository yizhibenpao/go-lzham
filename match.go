package lzham

import "math/bits"

// matchFinder is the upstream single-threaded binary-tree search accelerator.
// Positions in the trees are absolute uint32 offsets; byte storage is a ring.
type matchFinder struct {
	dict       []byte
	hash       [65536]uint32
	nodes      []uint32
	digramHash [4096]uint32
	digramNext []uint32
	matches    [][]dictMatch
	position   uint32
	available  int
	blockSize  int
}

type dictMatch struct {
	length   int
	distance uint32
}

func newMatchFinder() *matchFinder {
	return &matchFinder{
		dict:  make([]byte, dictSize+65536),
		nodes: make([]uint32, dictSize*2),
	}
}

func (f *matchFinder) fill(src []byte, models *decoder) {
	f.blockSize = len(src)
	addPos := int(f.position) & (dictSize - 1)
	copy(f.dict[addPos:], src)
	if addPos < 65536 {
		copy(f.dict[dictSize:], f.dict[:65536])
	}
	if limit := dictSize - len(src); f.available > limit {
		f.available = limit
	}
	f.matches = make([][]dictMatch, len(src))
	f.digramNext = make([]uint32, len(src))

	for i := 0; i+2 < len(src); i++ {
		absolute := f.position + uint32(i)
		insertPos := int(absolute) & (dictSize - 1)
		h := (uint32(f.dict[insertPos]) | uint32(f.dict[insertPos+1])<<8) ^ uint32(f.dict[insertPos+2])<<4
		current := f.hash[h]
		f.hash[h] = absolute
		left, right := insertPos*2, insertPos*2+1
		maxLength := minInt(maxMatchLen, len(src)-i)
		bestLength := 2
		var found []dictMatch
		for probe := 0; ; probe++ {
			distance := absolute - current
			if probe == 128 || distance == 0 || uint64(distance) >= uint64(f.available+i) {
				f.nodes[left], f.nodes[right] = 0, 0
				break
			}
			candidate := int(current) & (dictSize - 1)
			node := candidate * 2
			length := 0
			for length < maxLength && f.dict[candidate+length] == f.dict[insertPos+length] {
				length++
			}
			if length > bestLength {
				found = append(found, dictMatch{length, distance})
				bestLength = length
				if length == maxLength {
					f.nodes[left], f.nodes[right] = f.nodes[node], f.nodes[node+1]
					break
				}
			} else if bestLength > 2 && bestLength == length {
				best := &found[len(found)-1]
				bestSlot := positionSlot(models, uint(best.distance))
				compSlot := positionSlot(models, uint(distance))
				bestExtra := uint(best.distance) - models.slotBase[bestSlot]
				compExtra := uint(distance) - models.slotBase[compSlot]
				if compSlot < bestSlot || (compSlot >= 8 && compSlot == bestSlot && compExtra&15 < bestExtra&15) {
					best.distance = distance
				} else if length < maxLength && compSlot <= bestSlot {
					desired := f.dict[insertPos+length]
					old := f.dict[(insertPos-int(best.distance)+length)&(dictSize-1)]
					next := f.dict[candidate+length]
					if bits.OnesCount8(next^desired) < bits.OnesCount8(old^desired) {
						best.distance = distance
					}
				}
			}
			var next uint32
			if f.dict[candidate+length] < f.dict[insertPos+length] {
				f.nodes[left] = current
				left = node + 1
				next = f.nodes[node+1]
			} else {
				f.nodes[right] = current
				right = node
				next = f.nodes[node]
			}
			if next == current {
				break
			}
			current = next
		}
		f.matches[i] = found
	}
	for i := maxInt(0, len(src)-2); i < len(src); i++ {
		p := int(f.position+uint32(i)) & (dictSize - 1)
		f.nodes[p*2], f.nodes[p*2+1] = 0, 0
	}
	for i := 0; i+1 < len(src); i++ {
		p := addPos + i
		h := uint32(f.dict[p]) ^ uint32(f.dict[p+1])<<4
		f.digramNext[i] = f.digramHash[h]
		f.digramHash[h] = f.position + uint32(i)
	}
}

func (f *matchFinder) finish() {
	f.position += uint32(f.blockSize)
	f.available += f.blockSize
}

func (f *matchFinder) byteAt(position int) byte {
	return f.dict[position&(dictSize-1)]
}

func (f *matchFinder) length(offset, distance, maximum int) int {
	if distance > f.available+offset {
		return 0
	}
	p := (int(f.position) + offset) & (dictSize - 1)
	q := (int(f.position) + offset - distance) & (dictSize - 1)
	length := 0
	for length < maximum && f.dict[p+length] == f.dict[q+length] {
		length++
	}
	return length
}

func (f *matchFinder) lengthTwo(offset int) int {
	if f.blockSize-offset < 2 {
		return 0
	}
	position := f.position + uint32(offset)
	previous := f.digramNext[offset]
	distance := position - previous
	if distance == 0 || distance > 2047 || uint64(distance) > uint64(f.available+offset) {
		return 0
	}
	if f.byteAt(int(position)) == f.byteAt(int(previous)) &&
		f.byteAt(int(position)+1) == f.byteAt(int(previous)+1) {
		return int(distance)
	}
	return 0
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
