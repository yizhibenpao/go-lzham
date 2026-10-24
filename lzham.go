// Package lzham provides the Titanfall 2 LZHAM memory API without cgo.
package lzham

import (
	"errors"
	"hash/adler32"
	"hash/crc32"
	"sort"
)

const (
	dictSizeLog2 = 20
	dictSize     = 1 << dictSizeLog2

	blockSync = 0
	blockComp = 1
	blockRaw  = 2
	blockEOF  = 3

	numStates      = 12
	numLitStates   = 7
	numIsMatchBits = 6

	minMatchLen     = 2
	maxMatchLen     = 257
	numSpecial      = 2
	minPositionSlot = 1

	arithMinLen  = uint32(0x01000000)
	arithMaxLen  = uint32(0xffffffff)
	arithProbBit = 11
	arithProbMax = 1 << arithProbBit
	arithMoveBit = 5
)

var (
	errZeroLength        = errors.New("lzham: zero-length buffer")
	errOutputTooSmall    = errors.New("lzham: output buffer too small")
	errUnexpectedEOF     = errors.New("lzham: unexpected end of file")
	errBadCode           = errors.New("lzham: bad code")
	errBadRawBlock       = errors.New("lzham: bad raw block")
	errBadSyncBlock      = errors.New("lzham: bad sync block")
	errNotFinished       = errors.New("lzham: incomplete: more bytes available")
	errAdlerMismatch     = errors.New("lzham: failed adler32 checksum")
	errInvalidParameters = errors.New("lzham: invalid argument")
)

// Decompress decodes a Titanfall 2 LZHAM stream into dst.
//
// The function keeps the original tf2lzham signature. It writes at most len(dst)
// bytes and returns the number of bytes written together with the decoded
// stream's Adler-32 and IEEE CRC-32 values.
func Decompress(dst, src []byte) (n int, gotAdler32, gotCRC32 uint32, err error) {
	if len(dst) == 0 || len(src) == 0 {
		return 0, 0, 0, errors.New(errZeroLength.Error())
	}

	d := newDecoder(src, dst)
	n, gotAdler32, gotCRC32, err = d.decode()
	if err != nil {
		return 0, 0, 0, errors.New(err.Error())
	}
	return n, gotAdler32, gotCRC32, nil
}

// Compress encodes src as a valid Titanfall 2 LZHAM stream into dst.
//
// The encoder uses the original UBER deterministic parser and binary-tree
// match finder. It preserves the original API and checksum behavior without cgo.
func Compress(dst, src []byte) (n int, gotAdler32, gotCRC32 uint32, err error) {
	if len(dst) == 0 || len(src) == 0 {
		return 0, 0, 0, errors.New(errZeroLength.Error())
	}
	if uint64(len(src)) > uint64(^uint32(0)) {
		return 0, 0, 0, errors.New(errInvalidParameters.Error())
	}

	adler := adler32.New()
	// The native memory API finishes compression before checking dst. Keeping
	// that order preserves an untouched short destination and overlapping
	// src/dst support.
	w := dynamicBitWriter{}
	models, err := newEncoderModels()
	if err != nil {
		return 0, 0, 0, errors.New(err.Error())
	}
	finder := newMatchFinder()
	nodes := make([]parseNode, maxParseNodes+1)
	firstBlock := true
	for start := 0; start < len(src); {
		end := start + minInt(dictSize/8, len(src)-start)
		block := src[start:end]
		blockLen := len(block)
		adler.Write(block)
		gotAdler32 = adler.Sum32()
		// Preserve the upstream bug: each block starts CRC calculation from
		// the cumulative Adler-32, rather than from the previous CRC-32.
		gotCRC32 = crc32.Update(gotAdler32, crc32.IEEETable, block)
		finder.fill(block, models)
		compressed, nextModels, compErr := encodeOptimalBlock(finder, models, firstBlock, nodes)
		if compErr != nil {
			return 0, 0, 0, errors.New(compErr.Error())
		}
		if len(compressed) < blockLen {
			w.bytes(compressed)
			models = nextModels
		} else {
			if firstBlock {
				w.put(0, 2)
			}
			w.put(blockRaw, 2)
			w.put(uint32(blockLen-1), 24)
			w.put(uint32((blockLen-1)^((blockLen-1)>>8)^((blockLen-1)>>16)), 8)
			w.alignByte()
			w.bytes(block)
		}
		finder.finish()
		firstBlock = false
		start = end
	}

	w.put(blockEOF, 2)
	w.alignByte()
	w.put(gotAdler32, 32)
	w.put(gotCRC32, 32)
	if len(w.buf) > len(dst) {
		return 0, 0, 0, errors.New(errOutputTooSmall.Error())
	}
	copy(dst, w.buf)
	return len(w.buf), gotAdler32, gotCRC32, nil
}

func newEncoderModels() (*decoder, error) {
	models := newDecoder(nil, nil)
	if err := models.initModels(false, false); err != nil {
		return nil, err
	}
	return models, nil
}

func cloneEncoderModels(src *decoder) *decoder {
	dst := &decoder{
		numSlots: src.numSlots,
	}
	dst.slotBase = src.slotBase
	dst.slotExtra = src.slotExtra
	for i := range src.lit {
		dst.lit[i] = src.lit[i].clone()
		dst.deltaLit[i] = src.deltaLit[i].clone()
	}
	dst.main = src.main.clone()
	for i := range src.repLen {
		dst.repLen[i] = src.repLen[i].clone()
		dst.largeLen[i] = src.largeLen[i].clone()
	}
	dst.distLSB = src.distLSB.clone()
	dst.isMatch = src.isMatch
	dst.isRep = src.isRep
	dst.isRep0 = src.isRep0
	dst.isRep0SingleByte = src.isRep0SingleByte
	dst.isRep1 = src.isRep1
	dst.isRep2 = src.isRep2
	return dst
}

func positionSlot(models *decoder, distance uint) uint {
	for slot := uint(minPositionSlot); slot < models.numSlots; slot++ {
		if distance < models.slotBase[slot]+(1<<models.slotExtra[slot]) {
			return slot
		}
	}
	return models.numSlots - 1
}

type bitReader struct {
	src []byte
	pos uint64
}

func (r *bitReader) get(bits uint) (uint32, error) {
	if bits == 0 {
		return 0, nil
	}
	if bits > 32 {
		return 0, errUnexpectedEOF
	}
	var v uint32
	for i := uint(0); i < bits; i++ {
		v <<= 1
		// LZHAM's memory decoder zero-extends entropy-coded input at EOF.
		// Raw blocks separately require their actual payload bytes.
		if r.pos>>3 < uint64(len(r.src)) {
			v |= uint32((r.src[r.pos>>3] >> (7 - (r.pos & 7))) & 1)
		}
		r.pos++
	}
	return v, nil
}

func (r *bitReader) alignByte() {
	r.pos = (r.pos + 7) &^ 7
}

type dynamicBitWriter struct {
	buf []byte
	pos uint
}

func (w *dynamicBitWriter) put(value uint32, bits uint) {
	for i := bits; i > 0; i-- {
		if w.pos&7 == 0 {
			w.buf = append(w.buf, 0)
		}
		if value&(1<<(i-1)) != 0 {
			w.buf[len(w.buf)-1] |= 1 << (7 - (w.pos & 7))
		}
		w.pos++
	}
}

func (w *dynamicBitWriter) alignByte() {
	if padding := (-w.pos) & 7; padding != 0 {
		w.put(0, padding)
	}
}

func (w *dynamicBitWriter) bytes(src []byte) {
	w.alignByte()
	w.buf = append(w.buf, src...)
	w.pos += uint(len(src)) * 8
}

type encodedSymbol struct {
	kind uint8
	bits uint32
	n    uint
	bit  uint32
	prob uint32
}

const (
	encodedBits uint8 = iota
	encodedArithInit
	encodedArith
)

type symbolEncoder struct {
	symbols []encodedSymbol

	arithBase   uint32
	arithLength uint32
	arithOut    []byte
}

func (e *symbolEncoder) start() {
	e.symbols = e.symbols[:0]
	e.arithOut = e.arithOut[:0]
	e.arithBase = 0
	e.arithLength = arithMaxLen
}

func (e *symbolEncoder) putBits(bits uint32, n uint) bool {
	e.symbols = append(e.symbols, encodedSymbol{kind: encodedBits, bits: bits, n: n})
	return true
}

func (e *symbolEncoder) arithInit() {
	e.symbols = append(e.symbols, encodedSymbol{kind: encodedArithInit})
}

func (e *symbolEncoder) propagateCarry() {
	for i := len(e.arithOut) - 1; i >= 0; i-- {
		if e.arithOut[i] == 0xff {
			e.arithOut[i] = 0
			continue
		}
		e.arithOut[i]++
		break
	}
}

func (e *symbolEncoder) renorm() {
	for {
		e.arithOut = append(e.arithOut, byte(e.arithBase>>24))
		e.arithBase <<= 8
		e.arithLength <<= 8
		if e.arithLength >= arithMinLen {
			return
		}
	}
}

func (e *symbolEncoder) arithBit(bit uint32, model *arithmeticBitModel) {
	prob := model.prob0
	e.symbols = append(e.symbols, encodedSymbol{
		kind: encodedArith,
		bit:  bit,
		prob: prob,
	})
	x := uint32(uint64(prob) * uint64(e.arithLength>>arithProbBit))
	if bit == 0 {
		model.update(0)
		e.arithLength = x
	} else {
		model.update(1)
		orig := e.arithBase
		e.arithBase += x
		e.arithLength -= x
		if orig > e.arithBase {
			e.propagateCarry()
		}
	}
	if e.arithLength < arithMinLen {
		e.renorm()
	}
}

func (e *symbolEncoder) stop() {
	orig := e.arithBase
	if e.arithLength > 2*arithMinLen {
		e.arithBase += arithMinLen
		e.arithLength = arithMinLen >> 1
	} else {
		e.arithBase += arithMinLen >> 1
		e.arithLength = arithMinLen >> 9
	}
	if orig > e.arithBase {
		e.propagateCarry()
	}
	e.renorm()
	for len(e.arithOut) < 4 {
		e.arithOut = append(e.arithOut, 0)
	}
}

func (e *symbolEncoder) assemble() []byte {
	w := dynamicBitWriter{}
	arithOffset := 0
	arithLength := arithMaxLen

	for _, sym := range e.symbols {
		switch sym.kind {
		case encodedBits:
			w.put(sym.bits, sym.n)
		case encodedArithInit:
			arithLength = arithMaxLen
			for i := 0; i < 4; i++ {
				c := e.arithOut[arithOffset]
				arithOffset++
				w.put(uint32(c), 8)
			}
		case encodedArith:
			for arithLength < arithMinLen {
				var c byte
				if arithOffset < len(e.arithOut) {
					c = e.arithOut[arithOffset]
					arithOffset++
				}
				w.put(uint32(c), 8)
				arithLength <<= 8
			}
			x := uint32(uint64(sym.prob) * uint64(arithLength>>arithProbBit))
			if sym.bit == 0 {
				arithLength = x
			} else {
				arithLength -= x
			}
		}
	}

	// The C++ symbol codec flushes seven zero bits to make all pending
	// buffered bits observable, but it only retains complete bytes in the
	// output buffer. The decoder aligns before reading the next block.
	w.put(0, 7)
	return w.buf[:w.pos/8]
}

type arithmeticBitModel struct {
	prob0 uint32
}

func (m *arithmeticBitModel) reset() {
	m.prob0 = arithProbMax >> 1
}

func (m *arithmeticBitModel) update(bit uint32) {
	if bit == 0 {
		m.prob0 += (arithProbMax - m.prob0) >> arithMoveBit
	} else {
		m.prob0 -= m.prob0 >> arithMoveBit
	}
}

type arithmeticDecoder struct {
	r      *bitReader
	value  uint32
	length uint32
}

func (d *arithmeticDecoder) start() error {
	d.value = 0
	d.length = arithMaxLen
	for i := 0; i < 4; i++ {
		b, err := d.r.get(8)
		if err != nil {
			return err
		}
		d.value = d.value<<8 | b
	}
	return nil
}

func (d *arithmeticDecoder) bit(m *arithmeticBitModel) (uint32, error) {
	for d.length < arithMinLen {
		b, err := d.r.get(8)
		if err != nil {
			return 0, err
		}
		d.value = d.value<<8 | b
		d.length <<= 8
	}

	x := uint32((uint64(m.prob0) * uint64(d.length>>arithProbBit)))
	if d.value < x {
		m.update(0)
		d.length = x
		return 0, nil
	}
	m.update(1)
	d.value -= x
	d.length -= x
	return 1, nil
}

type huffmanModel struct {
	freq        []uint16
	codeSize    []uint8
	codes       []uint16
	firstCode   [17]uint32
	count       [17]uint32
	symbols     [17][]uint16
	maxCodeLen  uint
	updateRate  uint
	symbolsLeft uint
	totalCount  uint
	maxCycle    uint
	fast        bool
	polar       bool
}

func newHuffmanModel(total uint, fast, polar bool) (*huffmanModel, error) {
	if total == 0 || total > 1024 {
		return nil, errInvalidParameters
	}
	m := &huffmanModel{
		freq:     make([]uint16, total),
		codeSize: make([]uint8, total),
		codes:    make([]uint16, total),
		fast:     fast,
		polar:    polar,
	}
	if fast {
		m.maxCycle = (maxUint(64, total) + 6) << 5
	} else {
		m.maxCycle = (maxUint(24, total) + 6) * 12
	}
	if m.maxCycle > 32767 {
		m.maxCycle = 32767
	}
	if err := m.reset(); err != nil {
		return nil, err
	}
	return m, nil
}

func (m *huffmanModel) reset() error {
	for i := range m.freq {
		m.freq[i] = 1
	}
	m.totalCount = 0
	m.updateRate = uint(len(m.freq))
	m.symbolsLeft = 0
	if err := m.update(); err != nil {
		return err
	}
	m.symbolsLeft = 8
	m.updateRate = 8
	return nil
}

func (m *huffmanModel) resetUpdateRate() {
	m.totalCount += m.updateRate - m.symbolsLeft
	if m.totalCount > uint(len(m.freq)) {
		m.rescale()
	}
	if m.updateRate > 8 {
		m.updateRate = 8
	}
	m.symbolsLeft = m.updateRate
}

func (m *huffmanModel) rescale() {
	total := uint(0)
	for i, f := range m.freq {
		v := uint(f+1) >> 1
		m.freq[i] = uint16(v)
		total += v
	}
	m.totalCount = total
}

func (m *huffmanModel) updateSymbol(sym uint) error {
	if sym >= uint(len(m.freq)) {
		return errBadCode
	}
	m.freq[sym]++
	if m.symbolsLeft > 0 {
		m.symbolsLeft--
	}
	if m.symbolsLeft == 0 {
		return m.update()
	}
	return nil
}

func (m *huffmanModel) update() error {
	m.totalCount += m.updateRate
	for m.totalCount >= 32768 {
		m.rescale()
	}

	var err error
	if m.polar {
		err = generatePolarCodes(m.freq, m.codeSize)
	} else {
		err = generateHuffmanCodes(m.freq, m.codeSize)
	}
	if err != nil {
		return err
	}
	if err := limitCodeSizes(m.codeSize, 16); err != nil {
		return err
	}
	if err := m.buildCodes(); err != nil {
		return err
	}
	if m.fast {
		m.updateRate *= 2
	} else {
		m.updateRate = (5 * m.updateRate) >> 2
	}
	if m.updateRate > m.maxCycle {
		m.updateRate = m.maxCycle
	}
	m.symbolsLeft = m.updateRate
	return nil
}

func (m *huffmanModel) buildCodes() error {
	for i := range m.symbols {
		m.symbols[i] = nil
		m.count[i] = 0
		m.firstCode[i] = 0
	}
	var code uint32
	for bits := uint(1); bits <= 16; bits++ {
		m.firstCode[bits] = code
		for _, size := range m.codeSize {
			if uint(size) == bits {
				m.count[bits]++
			}
		}
		code = (code + m.count[bits]) << 1
	}
	if code != 1<<17 {
		total := uint32(0)
		for bits := uint(1); bits <= 16; bits++ {
			total += m.count[bits]
			if total > 1 {
				return errBadCode
			}
		}
	}
	for sym, size := range m.codeSize {
		if size > 16 {
			return errBadCode
		}
		if size != 0 {
			m.codes[sym] = uint16(m.firstCode[size] + uint32(len(m.symbols[size])))
			m.symbols[size] = append(m.symbols[size], uint16(sym))
		}
	}
	var maxLen uint
	for bits := uint(1); bits <= 16; bits++ {
		if len(m.symbols[bits]) != int(m.count[bits]) {
			return errBadCode
		}
		if m.count[bits] != 0 {
			maxLen = bits
		}
	}
	m.maxCodeLen = maxLen
	return nil
}

func (m *huffmanModel) encode(w *symbolEncoder, sym uint) error {
	if sym >= uint(len(m.freq)) || m.codeSize[sym] == 0 {
		return errBadCode
	}
	if !w.putBits(uint32(m.codes[sym]), uint(m.codeSize[sym])) {
		return errOutputTooSmall
	}
	return m.updateSymbol(sym)
}

func (m *huffmanModel) clone() *huffmanModel {
	c := &huffmanModel{
		freq:        append([]uint16(nil), m.freq...),
		codeSize:    append([]uint8(nil), m.codeSize...),
		codes:       append([]uint16(nil), m.codes...),
		firstCode:   m.firstCode,
		count:       m.count,
		maxCodeLen:  m.maxCodeLen,
		updateRate:  m.updateRate,
		symbolsLeft: m.symbolsLeft,
		totalCount:  m.totalCount,
		maxCycle:    m.maxCycle,
		fast:        m.fast,
		polar:       m.polar,
	}
	for i := range m.symbols {
		c.symbols[i] = append([]uint16(nil), m.symbols[i]...)
	}
	return c
}

func (m *huffmanModel) decode(r *bitReader) (uint, error) {
	var code uint32
	for bits := uint(1); bits <= m.maxCodeLen; bits++ {
		b, err := r.get(1)
		if err != nil {
			return 0, err
		}
		code = code<<1 | b
		first := m.firstCode[bits]
		if code >= first && code < first+m.count[bits] {
			syms := m.symbols[bits]
			sym := uint(syms[code-first])
			if err := m.updateSymbol(sym); err != nil {
				return 0, err
			}
			return sym, nil
		}
	}
	return 0, errBadCode
}

func maxUint(a, b uint) uint {
	if a > b {
		return a
	}
	return b
}

func generateHuffmanCodes(freq []uint16, sizes []uint8) error {
	type symbol struct {
		freq uint
		sym  int
	}
	used := make([]symbol, 0, len(freq))
	for i, f := range freq {
		sizes[i] = 0
		if f != 0 {
			used = append(used, symbol{freq: uint(f), sym: i})
		}
	}
	if len(used) == 0 {
		return errBadCode
	}
	if len(used) == 1 {
		sizes[used[0].sym] = 1
		return nil
	}
	sort.SliceStable(used, func(i, j int) bool {
		return used[i].freq < used[j].freq
	})

	depths := make([]int, len(used))
	for i := range used {
		depths[i] = int(used[i].freq)
	}
	calculateMinimumRedundancy(depths)
	for i, sym := range used {
		if depths[i] < 1 || depths[i] > 34 {
			return errBadCode
		}
		sizes[sym.sym] = uint8(depths[i])
	}
	return nil
}

// Port of calculate_minimum_redundancy by Alistair Moffat and
// Jyrki Katajainen, November 1996, as included in LZHAM.
func calculateMinimumRedundancy(a []int) {
	n := len(a)
	if n < 2 {
		return
	}
	a[0] += a[1]
	root, leaf := 0, 2
	for next := 1; next < n-1; next++ {
		if leaf >= n || a[root] < a[leaf] {
			a[next] = a[root]
			a[root] = next
			root++
		} else {
			a[next] = a[leaf]
			leaf++
		}
		if leaf >= n || (root < next && a[root] < a[leaf]) {
			a[next] += a[root]
			a[root] = next
			root++
		} else {
			a[next] += a[leaf]
			leaf++
		}
	}
	a[n-2] = 0
	for next := n - 3; next >= 0; next-- {
		a[next] = a[a[next]] + 1
	}
	available, used, depth, root, next := 1, 0, 0, n-2, n-1
	for available > 0 {
		for root >= 0 && a[root] == depth {
			used++
			root--
		}
		for available > used {
			a[next] = depth
			next--
			available--
		}
		available = 2 * used
		depth++
		used = 0
	}
}

type decoder struct {
	r   bitReader
	dst []byte
	pos int

	slotBase  [128]uint
	slotExtra [128]uint
	numSlots  uint

	lit      [64]*huffmanModel
	deltaLit [64]*huffmanModel
	main     *huffmanModel
	repLen   [2]*huffmanModel
	largeLen [2]*huffmanModel
	distLSB  *huffmanModel

	isMatch          [numStates * (1 << numIsMatchBits)]arithmeticBitModel
	isRep            [numStates]arithmeticBitModel
	isRep0           [numStates]arithmeticBitModel
	isRep0SingleByte [numStates]arithmeticBitModel
	isRep1           [numStates]arithmeticBitModel
	isRep2           [numStates]arithmeticBitModel
}

func newDecoder(src, dst []byte) *decoder {
	d := &decoder{r: bitReader{src: src}, dst: dst}
	d.initSlots()
	return d
}

func (d *decoder) initSlots() {
	j := uint(0)
	for i := uint(0); i < 128; i += 2 {
		d.slotExtra[i] = j
		d.slotExtra[i+1] = j
		if i != 0 && j < 25 {
			j++
		}
	}
	j = 0
	for i := uint(0); i < 128; i++ {
		d.slotBase[i] = j
		j += 1 << d.slotExtra[i]
	}
	maxDist := uint(dictSize - 1)
	for i := uint(0); i < 128; i++ {
		if maxDist >= d.slotBase[i] && maxDist < d.slotBase[i]+(1<<d.slotExtra[i]) {
			d.numSlots = i + 1
			break
		}
	}
}

func (d *decoder) initModels(fast, polar bool) error {
	for i := range d.lit {
		var err error
		d.lit[i], err = newHuffmanModel(256, fast, polar)
		if err != nil {
			return err
		}
		d.deltaLit[i], err = newHuffmanModel(256, fast, polar)
		if err != nil {
			return err
		}
	}
	var err error
	d.main, err = newHuffmanModel(numSpecial+(d.numSlots-minPositionSlot)*8, fast, polar)
	if err != nil {
		return err
	}
	for i := 0; i < 2; i++ {
		d.repLen[i], err = newHuffmanModel(1+(maxMatchLen-minMatchLen+1), fast, polar)
		if err != nil {
			return err
		}
		d.largeLen[i], err = newHuffmanModel(1+249, fast, polar)
		if err != nil {
			return err
		}
	}
	d.distLSB, err = newHuffmanModel(16, fast, polar)
	if err != nil {
		return err
	}
	d.resetArithmeticModels()
	return nil
}

func (d *decoder) resetArithmeticModels() {
	for i := range d.isMatch {
		d.isMatch[i].reset()
	}
	for i := 0; i < numStates; i++ {
		d.isRep[i].reset()
		d.isRep0[i].reset()
		d.isRep0SingleByte[i].reset()
		d.isRep1[i].reset()
		d.isRep2[i].reset()
	}
}

func (d *decoder) resetModels() error {
	for _, models := range [][]*huffmanModel{d.lit[:], d.deltaLit[:], []*huffmanModel{d.main}, d.repLen[:], d.largeLen[:], []*huffmanModel{d.distLSB}} {
		for _, model := range models {
			if err := model.reset(); err != nil {
				return err
			}
		}
	}
	d.resetArithmeticModels()
	return nil
}

func (d *decoder) resetUpdateRates() {
	for _, models := range [][]*huffmanModel{d.lit[:], d.deltaLit[:], []*huffmanModel{d.main}, d.repLen[:], d.largeLen[:], []*huffmanModel{d.distLSB}} {
		for _, model := range models {
			model.resetUpdateRate()
		}
	}
}

func (d *decoder) decode() (int, uint32, uint32, error) {
	config, err := d.r.get(2)
	if err != nil {
		return 0, 0, 0, err
	}
	if err = d.initModels((config&2) != 0, (config&1) != 0); err != nil {
		return 0, 0, 0, err
	}

	for {
		block, err := d.r.get(2)
		if err != nil {
			return 0, 0, 0, err
		}
		switch block {
		case blockSync:
			flush, err := d.r.get(2)
			if err != nil {
				return 0, 0, 0, err
			}
			switch flush {
			case 1:
				d.resetUpdateRates()
			case 2:
				if err := d.resetModels(); err != nil {
					return 0, 0, 0, err
				}
			}
			d.r.alignByte()
			a, err := d.r.get(16)
			if err != nil {
				return 0, 0, 0, err
			}
			b, err := d.r.get(16)
			if err != nil {
				return 0, 0, 0, err
			}
			if a != 0 || b != 0xffff {
				return 0, 0, 0, errBadSyncBlock
			}
			if flush == 2 {
				// The one-shot native API does not resume after FULL_FLUSH.
				return 0, 0, 0, errNotFinished
			}
		case blockRaw:
			rawLenMinusOne, err := d.r.get(24)
			if err != nil {
				return 0, 0, 0, err
			}
			check, err := d.r.get(8)
			if err != nil {
				return 0, 0, 0, err
			}
			if uint32(byte(rawLenMinusOne)^byte(rawLenMinusOne>>8)^byte(rawLenMinusOne>>16)) != check {
				return 0, 0, 0, errBadRawBlock
			}
			d.r.alignByte()
			rawLen := int(rawLenMinusOne + 1)
			if rawLen > len(d.dst)-d.pos {
				return 0, 0, 0, errOutputTooSmall
			}
			if d.r.pos/8 > uint64(len(d.r.src)) || uint64(rawLen) > uint64(len(d.r.src))-d.r.pos/8 {
				return 0, 0, 0, errUnexpectedEOF
			}
			offset := d.r.pos / 8
			copy(d.dst[d.pos:d.pos+rawLen], d.r.src[offset:offset+uint64(rawLen)])
			d.r.pos += uint64(rawLen) * 8
			d.pos += rawLen
		case blockComp:
			if err := d.decodeCompressedBlock(); err != nil {
				return 0, 0, 0, err
			}
			d.r.alignByte()
		case blockEOF:
			d.r.alignByte()
			wantAdler, err := d.r.get(32)
			if err != nil {
				return 0, 0, 0, err
			}
			gotAdler := adler32.Checksum(d.dst[:d.pos])
			gotCRC := crc32.ChecksumIEEE(d.dst[:d.pos])
			if wantAdler != gotAdler {
				return 0, gotAdler, gotCRC, errAdlerMismatch
			}
			return d.pos, gotAdler, gotCRC, nil
		default:
			return 0, 0, 0, errBadCode
		}
	}
}

func (d *decoder) decodeCompressedBlock() error {
	arith := arithmeticDecoder{r: &d.r}
	if err := arith.start(); err != nil {
		return err
	}

	flush, err := d.r.get(2)
	if err != nil {
		return err
	}
	switch flush {
	case 1:
		d.resetUpdateRates()
	case 2:
		if err := d.resetModels(); err != nil {
			return err
		}
	}

	matchHist := [4]uint{1, 1, 1, 1}
	curState := uint(0)
	prevChar, prevPrevChar := byte(0), byte(0)

	for {
		context := (uint(prevChar) >> (8 - numIsMatchBits)) + (curState << numIsMatchBits)
		isMatch, err := arith.bit(&d.isMatch[context])
		if err != nil {
			return err
		}
		if isMatch == 0 {
			if d.pos >= len(d.dst) {
				return errOutputTooSmall
			}
			var lit uint
			if curState < numLitStates {
				pred := (uint(prevChar) >> 5) | (uint(prevPrevChar) >> 5 << 3)
				lit, err = d.lit[pred].decode(&d.r)
				if err != nil {
					return err
				}
			} else {
				if matchHist[0] > uint(d.pos) || d.pos == 0 {
					return errBadCode
				}
				ref := d.pos - int(matchHist[0])
				if ref == d.pos || ref < 0 || ref >= len(d.dst) {
					return errBadCode
				}
				rep0 := d.dst[ref]
				rep1 := byte(0)
				if ref > 0 {
					rep1 = d.dst[ref-1]
				}
				pred := (uint(rep0) >> 5) | (uint(rep1) >> 5 << 3)
				lit, err = d.deltaLit[pred].decode(&d.r)
				if err != nil {
					return err
				}
				lit ^= uint(rep0)
			}
			if lit > 255 {
				return errBadCode
			}
			d.dst[d.pos] = byte(lit)
			prevPrevChar, prevChar = prevChar, byte(lit)
			curState = literalNextState[curState]
			d.pos++
			continue
		}

		isRep, err := arith.bit(&d.isRep[curState])
		if err != nil {
			return err
		}
		matchLen := uint(1)
		if isRep != 0 {
			isRep0, err := arith.bit(&d.isRep0[curState])
			if err != nil {
				return err
			}
			if isRep0 != 0 {
				single, err := arith.bit(&d.isRep0SingleByte[curState])
				if err != nil {
					return err
				}
				if single != 0 {
					if curState < numLitStates {
						curState = 9
					} else {
						curState = 11
					}
				} else {
					matchLen, err = d.decodeMatchLength(d.repLen[modelIndex(curState >= numLitStates)])
					if err != nil {
						return err
					}
					if curState < numLitStates {
						curState = 8
					} else {
						curState = 11
					}
				}
			} else {
				matchLen, err = d.decodeMatchLength(d.repLen[modelIndex(curState >= numLitStates)])
				if err != nil {
					return err
				}
				isRep1, err := arith.bit(&d.isRep1[curState])
				if err != nil {
					return err
				}
				if isRep1 != 0 {
					matchHist[0], matchHist[1] = matchHist[1], matchHist[0]
				} else {
					isRep2, err := arith.bit(&d.isRep2[curState])
					if err != nil {
						return err
					}
					if isRep2 != 0 {
						matchHist[0], matchHist[1], matchHist[2] = matchHist[2], matchHist[0], matchHist[1]
					} else {
						matchHist[0], matchHist[1], matchHist[2], matchHist[3] = matchHist[3], matchHist[0], matchHist[1], matchHist[2]
					}
				}
				if curState < numLitStates {
					curState = 8
				} else {
					curState = 11
				}
			}
		} else {
			sym, err := d.main.decode(&d.r)
			if err != nil {
				return err
			}
			if sym < numSpecial {
				if sym == 0 {
					break
				}
				matchHist = [4]uint{1, 1, 1, 1}
				curState = 0
				continue
			}
			sym -= numSpecial
			matchLen = (sym & 7) + minMatchLen
			matchSlot := (sym >> 3) + minPositionSlot
			if matchSlot >= d.numSlots {
				return errBadCode
			}
			if matchLen == 9 {
				extra, err := d.largeLen[modelIndex(curState >= numLitStates)].decode(&d.r)
				if err != nil {
					return err
				}
				matchLen += extra
				if matchLen == maxMatchLen+1 {
					matchLen, err = decodeHugeMatchLength(&d.r)
					if err != nil {
						return err
					}
				}
			}
			extraBits := uint(0)
			numExtra := d.slotExtra[matchSlot]
			if numExtra < 3 {
				value, err := d.r.get(numExtra)
				if err != nil {
					return err
				}
				extraBits = uint(value)
			} else {
				if numExtra > 4 {
					value, err := d.r.get(numExtra - 4)
					if err != nil {
						return err
					}
					extraBits = uint(value) << 4
				}
				value, err := d.distLSB.decode(&d.r)
				if err != nil {
					return err
				}
				extraBits += value
			}
			matchHist[3], matchHist[2], matchHist[1], matchHist[0] = matchHist[2], matchHist[1], matchHist[0], d.slotBase[matchSlot]+extraBits
			if curState < numLitStates {
				curState = numLitStates
			} else {
				curState = numLitStates + 3
			}
		}

		if matchHist[0] == 0 || matchHist[0] > uint(d.pos) || int(matchLen) > len(d.dst)-d.pos {
			return errBadCode
		}
		src := d.pos - int(matchHist[0])
		for i := uint(0); i < matchLen; i++ {
			if src+int(i) >= len(d.dst) {
				return errBadCode
			}
			d.dst[d.pos] = d.dst[src+int(i)]
			d.pos++
		}
		if matchLen == 1 {
			prevPrevChar = prevChar
			prevChar = d.dst[d.pos-1]
		} else {
			prevPrevChar = d.dst[d.pos-2]
			prevChar = d.dst[d.pos-1]
		}
	}
	return nil
}

func (d *decoder) decodeMatchLength(model *huffmanModel) (uint, error) {
	value, err := model.decode(&d.r)
	if err != nil {
		return 0, err
	}
	length := value + minMatchLen
	if length == maxMatchLen+1 {
		return decodeHugeMatchLength(&d.r)
	}
	return length, nil
}

func decodeHugeMatchLength(r *bitReader) (uint, error) {
	base := [...]uint{maxMatchLen + 1, maxMatchLen + 1 + 256, maxMatchLen + 1 + 256 + 1024, maxMatchLen + 1 + 256 + 1024 + 4096}
	bits := [...]uint{8, 10, 12, 16}
	category := uint(0)
	for category < 3 {
		b, err := r.get(1)
		if err != nil {
			return 0, err
		}
		if b == 0 {
			break
		}
		category++
	}
	value, err := r.get(bits[category])
	if err != nil {
		return 0, err
	}
	return base[category] + uint(value), nil
}

func modelIndex(matchState bool) int {
	if matchState {
		return 1
	}
	return 0
}

var literalNextState = [...]uint{0, 0, 0, 0, 1, 2, 3, 4, 5, 6, 4, 5}
