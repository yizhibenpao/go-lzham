package lzham

// The memory wrapper selects UBER + DETERMINISTIC_PARSING, with no helper
// threads or seed. This is the original single-state optimal parser, including
// its 64-byte fast-match shortcut and cost/complexity tie breaking.
const maxParseNodes = 3072

type lzDecision struct {
	position int
	length   int // zero denotes a literal
	distance int // -1..-4 denote rep0..rep3
}

type parseState struct {
	state   uint
	history [4]int
}

func initialParseState() parseState {
	return parseState{history: [4]int{1, 1, 1, 1}}
}

func (s *parseState) advance(d lzDecision) {
	if d.length == 0 {
		s.state = literalNextState[s.state]
		return
	}
	if d.distance < 0 {
		rep := -d.distance - 1
		distance := s.history[rep]
		copy(s.history[1:rep+1], s.history[:rep])
		s.history[0] = distance
		if s.state < numLitStates {
			s.state = 8
			if rep == 0 && d.length == 1 {
				s.state = 9
			}
		} else {
			s.state = 11
		}
		return
	}
	copy(s.history[1:], s.history[:3])
	s.history[0] = d.distance
	if s.state < numLitStates {
		s.state = numLitStates
	} else {
		s.state = numLitStates + 3
	}
}

func bitCost(model *arithmeticBitModel, bit uint32) uint64 {
	prob := model.prob0
	if bit != 0 {
		prob = arithProbMax - prob
	}
	return uint64(probabilityCost[prob])
}

func symbolCost(model *huffmanModel, symbol int) uint64 {
	return uint64(model.codeSize[symbol]) << 24
}

func previousByte(f *matchFinder, offset, backward int) byte {
	if backward > offset {
		return 0
	}
	return f.byteAt(int(f.position) + offset - backward)
}

func matchContext(f *matchFinder, offset int, state uint) uint {
	return uint(previousByte(f, offset, 1)>>2) + state<<numIsMatchBits
}

func literalCost(models *decoder, f *matchFinder, offset int, state parseState) uint64 {
	cost := bitCost(&models.isMatch[matchContext(f, offset, state.state)], 0)
	literal := f.byteAt(int(f.position) + offset)
	if state.state < numLitStates {
		p0, p1 := previousByte(f, offset, 1), previousByte(f, offset, 2)
		pred := uint(p0>>5) | uint(p1>>5)<<3
		return cost + symbolCost(models.lit[pred], int(literal))
	}
	ref := int(f.position) + offset - state.history[0]
	r0, r1 := f.byteAt(ref), f.byteAt(ref-1)
	pred := uint(r0>>5) | uint(r1>>5)<<3
	return cost + symbolCost(models.deltaLit[pred], int(literal^r0))
}

func repCosts(models *decoder, state parseState, context uint, rep, minimum, maximum int, costs *[maxMatchLen + 1]uint64) {
	s := state.state
	base := bitCost(&models.isMatch[context], 1) + bitCost(&models.isRep[s], 1)
	if rep == 0 {
		base += bitCost(&models.isRep0[s], 1)
		if minimum == 1 {
			costs[1] = base + bitCost(&models.isRep0SingleByte[s], 1)
			minimum++
		}
		base += bitCost(&models.isRep0SingleByte[s], 0)
	} else {
		base += bitCost(&models.isRep0[s], 0)
		if rep == 1 {
			base += bitCost(&models.isRep1[s], 1)
		} else {
			base += bitCost(&models.isRep1[s], 0)
			if rep == 2 {
				base += bitCost(&models.isRep2[s], 1)
			} else {
				base += bitCost(&models.isRep2[s], 0)
			}
		}
	}
	table := models.repLen[modelIndex(s >= numLitStates)]
	for length := minimum; length <= maximum; length++ {
		costs[length] = base + symbolCost(table, length-minMatchLen)
	}
}

func fullCosts(models *decoder, state parseState, context uint, distance, minimum, maximum int, costs *[maxMatchLen + 1]uint64) {
	s := state.state
	base := bitCost(&models.isMatch[context], 1) + bitCost(&models.isRep[s], 0)
	slot := positionSlot(models, uint(distance))
	extra := uint(distance) - models.slotBase[slot]
	numExtra := models.slotExtra[slot]
	if numExtra < 3 {
		base += uint64(numExtra) << 24
	} else {
		if numExtra > 4 {
			base += uint64(numExtra-4) << 24
		}
		base += symbolCost(models.distLSB, int(extra&15))
	}
	table := models.largeLen[modelIndex(s >= numLitStates)]
	for length := minimum; length <= maximum; length++ {
		cost := base
		low := length - minMatchLen
		if length >= 9 {
			low = 7
			cost += symbolCost(table, length-9)
		}
		symbol := numSpecial + int(slot-minPositionSlot)*8 + low
		costs[length] = cost + symbolCost(models.main, symbol)
	}
}

type parseNode struct {
	cost       uint64
	complexity uint32
	parent     int
	decision   lzDecision
	saved      parseState
}

func optimalParse(models *decoder, f *matchFinder, start, size int, initial parseState, nodes []parseNode) []lzDecision {
	for i := 1; i <= size; i++ {
		nodes[i].cost = ^uint64(0)
		nodes[i].complexity = ^uint32(0)
	}
	nodes[0] = parseNode{parent: -1}
	state := initial
	var costs [maxMatchLen + 1]uint64
	// Save the parent state, exactly as the C++ optimal parser does. Models
	// remain fixed during a graph search; only the eventual parse updates them.
	set := func(from, length, distance int, cost uint64, complexity uint32) {
		dst := &nodes[from+maxInt(length, 1)]
		if cost < dst.cost || (cost == dst.cost && complexity < dst.complexity) {
			dst.cost, dst.complexity = cost, complexity
			dst.parent = from
			dst.saved = state
			dst.decision = lzDecision{start + from, length, distance}
		}
	}
	for index := 0; index < size; {
		current := &nodes[index]
		if index != 0 {
			state = current.saved
			state.advance(current.decision)
		}
		offset := start + index
		maximum := minInt(maxMatchLen, size-index)
		context := matchContext(f, offset, state.state)
		cost, complexity := current.cost, current.complexity
		repMaximum := 0
		for rep, distance := range state.history {
			length := f.length(offset, distance, maximum)
			minimum := minMatchLen
			if rep == 0 {
				minimum = 1
			}
			if length < minimum {
				continue
			}
			repMaximum = maxInt(repMaximum, length)
			repCosts(models, state, context, rep, minimum, length, &costs)
			for n := minimum; n <= length; n++ {
				set(index, n, -rep-1, cost+costs[n], complexity+uint32(2+rep))
			}
		}
		if repMaximum >= 64 {
			index += repMaximum
			continue
		}
		maximumMatch := repMaximum
		if maximum >= minMatchLen {
			if repMaximum < 2 {
				if distance := f.lengthTwo(offset); distance != 0 {
					fullCosts(models, state, context, distance, 2, 2, &costs)
					set(index, 2, distance, cost+costs[2], complexity+7)
					maximumMatch = 2
				}
			}
			previousMaximum := maxInt(1, maximumMatch)
			for _, match := range f.matches[offset] {
				length := minInt(match.length, maximum)
				if length <= maximumMatch {
					continue
				}
				maximumMatch = length
				first := previousMaximum + 1
				fullCosts(models, state, context, int(match.distance), first, length, &costs)
				for n := first; n <= length; n++ {
					c := uint32(7)
					if n >= 9 {
						c = 6
					}
					set(index, n, int(match.distance), cost+costs[n], complexity+c)
				}
				previousMaximum = length
			}
		}
		if maximumMatch >= 64 {
			index += maximumMatch
			continue
		}
		set(index, 0, 0, cost+literalCost(models, f, offset, state), complexity+1)
		index++
	}
	decisions := make([]lzDecision, 0, size)
	for index := size; index > 0; index = nodes[index].parent {
		decisions = append(decisions, nodes[index].decision)
	}
	return decisions
}

func encodeLength(e *symbolEncoder, model *huffmanModel, length, base int) error {
	if length <= maxMatchLen {
		return model.encode(e, uint(length-base))
	}
	if err := model.encode(e, maxMatchLen+1-uint(base)); err != nil {
		return err
	}
	bases := [...]int{258, 514, 1538, 5634}
	counts := [...]uint{8, 10, 12, 16}
	category := 0
	for category < 3 && length >= bases[category+1] {
		category++
	}
	prefix := uint32(0)
	if category == 1 {
		prefix = 2
	} else if category == 2 {
		prefix = 6
	} else if category == 3 {
		prefix = 7
	}
	prefixBits := uint(category + 1)
	if category == 3 {
		prefixBits = 3
	}
	e.putBits(prefix, prefixBits)
	e.putBits(uint32(length-bases[category]), counts[category])
	return nil
}

func encodeDecision(e *symbolEncoder, models *decoder, f *matchFinder, state *parseState, d lzDecision) error {
	s := state.state
	context := matchContext(f, d.position, s)
	if d.length == 0 {
		e.arithBit(0, &models.isMatch[context])
		literal := f.byteAt(int(f.position) + d.position)
		if s < numLitStates {
			p0, p1 := previousByte(f, d.position, 1), previousByte(f, d.position, 2)
			pred := uint(p0>>5) | uint(p1>>5)<<3
			if err := models.lit[pred].encode(e, uint(literal)); err != nil {
				return err
			}
		} else {
			ref := int(f.position) + d.position - state.history[0]
			r0, r1 := f.byteAt(ref), f.byteAt(ref-1)
			pred := uint(r0>>5) | uint(r1>>5)<<3
			if err := models.deltaLit[pred].encode(e, uint(literal^r0)); err != nil {
				return err
			}
		}
		state.advance(d)
		return nil
	}
	e.arithBit(1, &models.isMatch[context])
	if d.distance < 0 {
		rep := -d.distance - 1
		e.arithBit(1, &models.isRep[s])
		if rep == 0 {
			e.arithBit(1, &models.isRep0[s])
			if d.length == 1 {
				e.arithBit(1, &models.isRep0SingleByte[s])
			} else {
				e.arithBit(0, &models.isRep0SingleByte[s])
				if err := encodeLength(e, models.repLen[modelIndex(s >= numLitStates)], d.length, 2); err != nil {
					return err
				}
			}
		} else {
			e.arithBit(0, &models.isRep0[s])
			if err := encodeLength(e, models.repLen[modelIndex(s >= numLitStates)], d.length, 2); err != nil {
				return err
			}
			if rep == 1 {
				e.arithBit(1, &models.isRep1[s])
			} else {
				e.arithBit(0, &models.isRep1[s])
				if rep == 2 {
					e.arithBit(1, &models.isRep2[s])
				} else {
					e.arithBit(0, &models.isRep2[s])
				}
			}
		}
	} else {
		e.arithBit(0, &models.isRep[s])
		slot := positionSlot(models, uint(d.distance))
		low := d.length - minMatchLen
		if d.length >= 9 {
			low = 7
		}
		symbol := numSpecial + (slot-minPositionSlot)*8 + uint(low)
		if err := models.main.encode(e, symbol); err != nil {
			return err
		}
		if d.length >= 9 {
			if err := encodeLength(e, models.largeLen[modelIndex(s >= numLitStates)], d.length, 9); err != nil {
				return err
			}
		}
		extra, count := uint(d.distance)-models.slotBase[slot], models.slotExtra[slot]
		if count < 3 {
			e.putBits(uint32(extra), count)
		} else {
			if count > 4 {
				e.putBits(uint32(extra>>4), count-4)
			}
			if err := models.distLSB.encode(e, extra&15); err != nil {
				return err
			}
		}
	}
	state.advance(d)
	return nil
}

func encodeOptimalBlock(f *matchFinder, base *decoder, first bool, nodes []parseNode) ([]byte, *decoder, error) {
	models := cloneEncoderModels(base)
	e := symbolEncoder{}
	e.start()
	if first {
		e.putBits(0, 2)
	}
	e.putBits(blockComp, 2)
	e.arithInit()
	e.putBits(0, 2)
	state := initialParseState()
	for offset := 0; offset < f.blockSize; {
		size := minInt(maxParseNodes, f.blockSize-offset)
		if first && offset < maxParseNodes {
			size = minInt(size, 1536)
		}
		decisions := optimalParse(models, f, offset, size, state, nodes)
		for i := len(decisions) - 1; i >= 0; i-- {
			if err := encodeDecision(&e, models, f, &state, decisions[i]); err != nil {
				return nil, base, err
			}
		}
		offset += size
	}
	context := matchContext(f, f.blockSize, state.state)
	e.arithBit(1, &models.isMatch[context])
	e.arithBit(0, &models.isRep[state.state])
	if err := models.main.encode(&e, 0); err != nil {
		return nil, base, err
	}
	e.stop()
	return e.assemble(), models, nil
}
