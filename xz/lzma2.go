/*
 *  Copyright (c) 2021-2026 Mikhail Knyazhev <markus621@yandex.ru>. All rights reserved.
 *  Use of this source code is governed by a BSD 3-Clause license that can be found in the LICENSE file.
 */

package xz

import (
	"bytes"
	"fmt"
	"io"
)

const (
	probBits        = 11
	probInit        = 1 << (probBits - 1)
	numStates       = 12
	numPosBitsMax   = 4
	numPosStates    = 1 << numPosBitsMax
	numLenToPos     = 4
	numPosSlotBits  = 6
	startPosModel   = 4
	endPosModel     = 14
	numFullDistance = 1 << (endPosModel / 2)
	numAlignBits    = 4
	matchMinLen     = 2
	matchMaxLen     = 273
	chunkSize       = 64 << 10
)

type countedByteReader struct {
	r interface{ ReadByte() (byte, error) }
	n int64
}

func (r *countedByteReader) ReadByte() (byte, error) {
	b, err := r.r.ReadByte()
	if err == nil {
		r.n++
	}
	return b, err
}

type limitedByteReader struct {
	r         interface{ ReadByte() (byte, error) }
	remaining int64
}

func (r *limitedByteReader) ReadByte() (byte, error) {
	if r.remaining == 0 {
		return 0, io.ErrUnexpectedEOF
	}
	b, err := r.r.ReadByte()
	if err != nil {
		return 0, err
	}
	r.remaining--
	return b, nil
}

type chunkReader struct {
	r         *countedByteReader
	remaining int64
}

func (r *chunkReader) ReadByte() (byte, error) {
	if r.remaining == 0 {
		return 0, io.ErrUnexpectedEOF
	}
	b, err := r.r.ReadByte()
	if err != nil {
		return 0, err
	}
	r.remaining--
	return b, nil
}

func (r *chunkReader) discard() error {
	var buf [256]byte
	for r.remaining > 0 {
		n := len(buf)
		if int64(n) > r.remaining {
			n = int(r.remaining)
		}
		for i := 0; i < n; i++ {
			if _, err := r.ReadByte(); err != nil {
				return err
			}
		}
	}
	return nil
}

type dictionary struct {
	data   []byte
	pos    int
	filled int
}

func newDictionary(size int64) (*dictionary, error) {
	if size < 4096 || size > int64(int(^uint(0)>>1)) {
		return nil, ErrResourceLimit
	}
	return &dictionary{data: make([]byte, int(size))}, nil
}

func (d *dictionary) reset() {
	d.pos = 0
	d.filled = 0
}

func (d *dictionary) put(b byte) {
	d.data[d.pos] = b
	d.pos++
	if d.pos == len(d.data) {
		d.pos = 0
	}
	if d.filled < len(d.data) {
		d.filled++
	}
}

func (d *dictionary) get(distance uint32) (byte, bool) {
	if uint64(distance) >= uint64(d.filled) || uint64(distance) >= uint64(len(d.data)) {
		return 0, false
	}
	i := d.pos - int(distance) - 1
	if i < 0 {
		i += len(d.data)
	}
	return d.data[i], true
}

type decodeOutput struct {
	dst   io.Writer
	buf   []byte
	count int64
	max   int64
	sum   *checksum
}

func (o *decodeOutput) emit(b byte) error {
	if o.max >= 0 && o.count >= o.max {
		return ErrResourceLimit
	}
	o.buf = append(o.buf, b)
	o.count++
	if len(o.buf) == cap(o.buf) {
		return o.flush()
	}
	return nil
}

func (o *decodeOutput) flush() error {
	if len(o.buf) == 0 {
		return nil
	}
	if err := writeAll(o.dst, o.buf); err != nil {
		return err
	}
	if o.sum != nil {
		o.sum.Write(o.buf)
	}
	o.buf = o.buf[:0]
	return nil
}

type lzmaState struct {
	dict *dictionary

	lc, lp, pb int

	state int
	rep   [4]uint32

	isMatch    []uint16
	isRep      []uint16
	isRepG0    []uint16
	isRepG1    []uint16
	isRepG2    []uint16
	isRep0Long []uint16
	posSlot    []uint16
	posSpecial []uint16
	posAlign   []uint16
	lenChoice  []uint16
	lenLow     []uint16
	lenMid     []uint16
	lenHigh    []uint16
	repChoice  []uint16
	repLow     []uint16
	repMid     []uint16
	repHigh    []uint16
	literal    []uint16
}

func initProbabilities(p []uint16) {
	for i := range p {
		p[i] = probInit
	}
}

func newLZMAState(dictSize int64) (*lzmaState, error) {
	dict, err := newDictionary(dictSize)
	if err != nil {
		return nil, err
	}
	return &lzmaState{
		dict:       dict,
		isMatch:    make([]uint16, numStates*numPosStates),
		isRep:      make([]uint16, numStates),
		isRepG0:    make([]uint16, numStates),
		isRepG1:    make([]uint16, numStates),
		isRepG2:    make([]uint16, numStates),
		isRep0Long: make([]uint16, numStates*numPosStates),
		posSlot:    make([]uint16, numLenToPos*(1<<numPosSlotBits)),
		posSpecial: make([]uint16, numFullDistance-endPosModel),
		posAlign:   make([]uint16, 1<<numAlignBits),
		lenChoice:  make([]uint16, 2),
		lenLow:     make([]uint16, numPosStates*8),
		lenMid:     make([]uint16, numPosStates*8),
		lenHigh:    make([]uint16, 1<<8),
		repChoice:  make([]uint16, 2),
		repLow:     make([]uint16, numPosStates*8),
		repMid:     make([]uint16, numPosStates*8),
		repHigh:    make([]uint16, 1<<8),
	}, nil
}

func (s *lzmaState) setProperties(props byte) error {
	v := int(props)
	lc := v % 9
	v /= 9
	lp := v % 5
	pb := v / 5
	if pb > numPosBitsMax || lc > 8 || lp > 4 {
		return ErrInvalidFormat
	}
	s.lc, s.lp, s.pb = lc, lp, pb
	s.literal = make([]uint16, (1<<(lc+lp))*0x300)
	s.resetState()
	return nil
}

func (s *lzmaState) resetState() {
	s.state = 0
	s.rep = [4]uint32{}
	initProbabilities(s.isMatch)
	initProbabilities(s.isRep)
	initProbabilities(s.isRepG0)
	initProbabilities(s.isRepG1)
	initProbabilities(s.isRepG2)
	initProbabilities(s.isRep0Long)
	initProbabilities(s.posSlot)
	initProbabilities(s.posSpecial)
	initProbabilities(s.posAlign)
	initProbabilities(s.lenChoice)
	initProbabilities(s.lenLow)
	initProbabilities(s.lenMid)
	initProbabilities(s.lenHigh)
	initProbabilities(s.repChoice)
	initProbabilities(s.repLow)
	initProbabilities(s.repMid)
	initProbabilities(s.repHigh)
	initProbabilities(s.literal)
}

func (s *lzmaState) decodeLiteralChecked(rc *rangeDecoder, position int64, previous byte) (byte, error) {
	ctx := int(((position & int64((1<<s.lp)-1)) << s.lc) | int64(previous>>uint(8-s.lc)))
	p := s.literal[ctx*0x300 : (ctx+1)*0x300]
	symbol := uint32(1)
	if s.state >= 7 {
		match, ok := s.dict.get(s.rep[0])
		if !ok {
			return 0, fmt.Errorf("literal match distance %d: %w", s.rep[0], ErrInvalidFormat)
		}
		for symbol < 0x100 {
			matchBit := uint32((match >> 7) & 1)
			match <<= 1
			bit, err := rc.decodeBit(&p[((1+matchBit)<<8)+symbol])
			if err != nil {
				return 0, err
			}
			symbol = (symbol << 1) | bit
			if matchBit != bit {
				for symbol < 0x100 {
					bit, err = rc.decodeBit(&p[symbol])
					if err != nil {
						return 0, err
					}
					symbol = (symbol << 1) | bit
				}
				return byte(symbol), nil
			}
		}
		return byte(symbol), nil
	}
	for symbol < 0x100 {
		bit, err := rc.decodeBit(&p[symbol])
		if err != nil {
			return 0, err
		}
		symbol = (symbol << 1) | bit
	}
	return byte(symbol), nil
}

func (s *lzmaState) decodeLen(rc *rangeDecoder, posState int) (int, error) {
	if bit, err := rc.decodeBit(&s.lenChoice[0]); err != nil {
		return 0, err
	} else if bit == 0 {
		v, err := rc.decodeTree(s.lenLow[posState*8:], 3)
		return int(v), err
	}
	if bit, err := rc.decodeBit(&s.lenChoice[1]); err != nil {
		return 0, err
	} else if bit == 0 {
		v, err := rc.decodeTree(s.lenMid[posState*8:], 3)
		return int(v) + 8, err
	}
	v, err := rc.decodeTree(s.lenHigh, 8)
	return int(v) + 16, err
}

func (s *lzmaState) decodeRepLen(rc *rangeDecoder, posState int) (int, error) {
	if bit, err := rc.decodeBit(&s.repChoice[0]); err != nil {
		return 0, err
	} else if bit == 0 {
		v, err := rc.decodeTree(s.repLow[posState*8:], 3)
		return int(v), err
	}
	if bit, err := rc.decodeBit(&s.repChoice[1]); err != nil {
		return 0, err
	} else if bit == 0 {
		v, err := rc.decodeTree(s.repMid[posState*8:], 3)
		return int(v) + 8, err
	}
	v, err := rc.decodeTree(s.repHigh, 8)
	return int(v) + 16, err
}

func (s *lzmaState) decodeOne(rc *rangeDecoder, out *decodeOutput, position int64, previous *byte) error {
	posState := int(position & int64((1<<s.pb)-1))
	matchBit, err := rc.decodeBit(&s.isMatch[s.state*numPosStates+posState])
	if err != nil {
		return err
	}
	if matchBit == 0 {
		return s.decodeLiteral(rc, out, position, previous)
	}
	length, matchLength, err := s.decodeMatch(rc, posState)
	if err != nil {
		return err
	}
	if matchLength == 0 {
		if length < 0 || length > matchMaxLen-matchMinLen {
			return ErrInvalidFormat
		}
		matchLength = length + matchMinLen
	}
	if uint64(s.rep[0]) >= uint64(s.dict.filled) {
		return fmt.Errorf("match at position %d distance %d with dictionary size %d: %w", position, s.rep[0], s.dict.filled, ErrInvalidFormat)
	}
	return s.emitMatch(out, previous, matchLength)
}

func (s *lzmaState) decodeLiteral(rc *rangeDecoder, out *decodeOutput, position int64, previous *byte) error {
	b, err := s.decodeLiteralChecked(rc, position, *previous)
	if err != nil {
		return err
	}
	s.dict.put(b)
	if err := out.emit(b); err != nil {
		return err
	}
	*previous = b
	s.state = literalState(s.state)
	return nil
}

func literalState(state int) int {
	switch {
	case state < 4:
		return 0
	case state < 10:
		return state - 3
	default:
		return state - 6
	}
}

func (s *lzmaState) decodeMatch(rc *rangeDecoder, posState int) (int, int, error) {
	isRep, err := rc.decodeBit(&s.isRep[s.state])
	if err != nil {
		return 0, 0, err
	}
	if isRep == 1 {
		return s.decodeRepMatch(rc, posState)
	}
	return s.decodeNewMatch(rc, posState)
}

func (s *lzmaState) decodeRepMatch(rc *rangeDecoder, posState int) (int, int, error) {
	bit, err := rc.decodeBit(&s.isRepG0[s.state])
	if err != nil {
		return 0, 0, err
	}
	if bit == 0 {
		bit, err = rc.decodeBit(&s.isRep0Long[s.state*numPosStates+posState])
		if err != nil {
			return 0, 0, err
		}
		if bit == 0 {
			if s.state < 7 {
				s.state = 9
			} else {
				s.state = 11
			}
			return 0, 1, nil
		}
		if s.state < 7 {
			s.state = 8
		} else {
			s.state = 11
		}
		length, err := s.decodeRepLen(rc, posState)
		return length, 0, err
	}

	distance, err := s.decodeRepDistance(rc)
	if err != nil {
		return 0, 0, err
	}
	s.rep[1] = s.rep[0]
	s.rep[0] = distance
	if s.state < 7 {
		s.state = 8
	} else {
		s.state = 11
	}
	length, err := s.decodeRepLen(rc, posState)
	return length, 0, err
}

func (s *lzmaState) decodeRepDistance(rc *rangeDecoder) (uint32, error) {
	bit, err := rc.decodeBit(&s.isRepG1[s.state])
	if err != nil {
		return 0, err
	}
	if bit == 0 {
		return s.rep[1], nil
	}
	bit, err = rc.decodeBit(&s.isRepG2[s.state])
	if err != nil {
		return 0, err
	}
	if bit == 0 {
		distance := s.rep[2]
		s.rep[2] = s.rep[1]
		return distance, nil
	}
	distance := s.rep[3]
	s.rep[3] = s.rep[2]
	s.rep[2] = s.rep[1]
	return distance, nil
}

func (s *lzmaState) decodeNewMatch(rc *rangeDecoder, posState int) (int, int, error) {
	s.rep[3] = s.rep[2]
	s.rep[2] = s.rep[1]
	s.rep[1] = s.rep[0]
	if s.state < 7 {
		s.state = 7
	} else {
		s.state = 10
	}
	length, err := s.decodeLen(rc, posState)
	if err != nil {
		return 0, 0, err
	}
	lenToPosState := length
	if lenToPosState > 3 {
		lenToPosState = 3
	}
	posSlot, err := rc.decodeTree(s.posSlot[lenToPosState*64:], 6)
	if err != nil {
		return 0, 0, err
	}
	distance, err := s.decodeNewDistance(rc, posSlot)
	if err != nil {
		return 0, 0, err
	}
	s.rep[0] = distance
	return length, 0, nil
}

func (s *lzmaState) decodeNewDistance(rc *rangeDecoder, posSlot uint32) (uint32, error) {
	if posSlot < startPosModel {
		return posSlot, nil
	}
	directBits := int(posSlot/2) - 1
	distance := uint32(2 | (posSlot & 1))
	if posSlot < endPosModel {
		distance <<= uint(directBits)
		value, err := rc.reverseSpecial(s.posSpecial, int(posSlot), directBits)
		if err != nil {
			return 0, err
		}
		return distance + value, nil
	}
	distance <<= uint(directBits)
	value, err := rc.decodeDirect(directBits - numAlignBits)
	if err != nil {
		return 0, err
	}
	align, err := rc.reverseTree(s.posAlign, numAlignBits)
	if err != nil {
		return 0, err
	}
	return distance + (value<<numAlignBits | align), nil
}

func (s *lzmaState) emitMatch(out *decodeOutput, previous *byte, length int) error {
	for i := 0; i < length; i++ {
		b, ok := s.dict.get(s.rep[0])
		if !ok {
			return ErrInvalidFormat
		}
		if err := out.emit(b); err != nil {
			return err
		}
		s.dict.put(b)
		*previous = b
	}
	return nil
}

func decodeLZMA2(in *countedByteReader, out *decodeOutput, dictSize int64) (int64, error) {
	s, err := newLZMAState(dictSize)
	if err != nil {
		return 0, err
	}
	var previous byte
	var position int64
	propsSet := false
	for {
		control, err := in.ReadByte()
		if err != nil {
			return 0, err
		}
		if control == 0 {
			return in.n, out.flush()
		}
		if control == 1 || control == 2 {
			if err := decodeUncompressedChunk(in, out, s, control, &previous, &position); err != nil {
				return 0, err
			}
			continue
		}
		if control < 0x80 {
			return 0, ErrInvalidFormat
		}
		uncompressed, compressed, err := readCompressedChunk(in, s, control, &propsSet, &previous)
		if err != nil {
			return 0, err
		}
		if err := decodeCompressedChunk(in, out, s, uncompressed, compressed, &previous, &position); err != nil {
			return 0, err
		}
	}
}

func decodeUncompressedChunk(in *countedByteReader, out *decodeOutput, s *lzmaState, control byte, previous *byte, position *int64) error {
	hi, err := in.ReadByte()
	if err != nil {
		return err
	}
	lo, err := in.ReadByte()
	if err != nil {
		return err
	}
	if control == 1 {
		s.dict.reset()
	}
	size := (int(hi)<<8 | int(lo)) + 1
	for n := 0; n < size; n++ {
		b, err := in.ReadByte()
		if err != nil {
			return err
		}
		s.dict.put(b)
		if err := out.emit(b); err != nil {
			return err
		}
		*previous = b
		(*position)++
	}
	return nil
}

func readCompressedChunk(in *countedByteReader, s *lzmaState, control byte, propsSet *bool, previous *byte) (int64, int64, error) {
	uncompressed, err := readChunkSize(in, control&0x1f)
	if err != nil {
		return 0, 0, err
	}
	compressed, err := readChunkSize(in, 0)
	if err != nil {
		return 0, 0, err
	}
	if control&0x40 != 0 {
		props, err := in.ReadByte()
		if err != nil {
			return 0, 0, err
		}
		if err := s.setProperties(props); err != nil {
			return 0, 0, err
		}
		*propsSet = true
	} else if !*propsSet {
		return 0, 0, ErrInvalidFormat
	}
	if control&0x20 != 0 {
		s.resetState()
	}
	if control&0xe0 == 0xe0 {
		s.dict.reset()
		*previous = 0
	}
	return uncompressed, compressed, nil
}

func readChunkSize(in *countedByteReader, high byte) (int64, error) {
	first, err := in.ReadByte()
	if err != nil {
		return 0, err
	}
	second, err := in.ReadByte()
	if err != nil {
		return 0, err
	}
	return (int64(high)<<16 | int64(first)<<8 | int64(second)) + 1, nil
}

func decodeCompressedChunk(in *countedByteReader, out *decodeOutput, s *lzmaState, uncompressed, compressed int64, previous *byte, position *int64) error {
	chunk := &chunkReader{r: in, remaining: compressed}
	rc, err := newRangeDecoder(chunk)
	if err != nil {
		return err
	}
	chunkOutputStart := out.count
	for out.count-chunkOutputStart < uncompressed {
		before := out.count
		if err := s.decodeOne(rc, out, *position, previous); err != nil {
			return err
		}
		produced := out.count - before
		if produced <= 0 || out.count-chunkOutputStart > uncompressed {
			return ErrInvalidFormat
		}
		*position += produced
	}
	return chunk.discard()
}

type rangeDecoder struct {
	in   *chunkReader
	code uint32
	rng  uint32
}

func newRangeDecoder(in *chunkReader) (*rangeDecoder, error) {
	r := &rangeDecoder{in: in, rng: ^uint32(0)}
	for i := 0; i < 5; i++ {
		b, err := in.ReadByte()
		if err != nil {
			return nil, err
		}
		r.code = r.code<<8 | uint32(b)
	}
	return r, nil
}

func (r *rangeDecoder) normalize() error {
	if r.rng >= 1<<24 {
		return nil
	}
	r.rng <<= 8
	b, err := r.in.ReadByte()
	if err != nil {
		return err
	}
	r.code = r.code<<8 | uint32(b)
	return nil
}

func (r *rangeDecoder) decodeBit(prob *uint16) (uint32, error) {
	if err := r.normalize(); err != nil {
		return 0, err
	}
	bound := (r.rng >> probBits) * uint32(*prob)
	var bit uint32
	if r.code < bound {
		r.rng = bound
		*prob += uint16((1<<probBits)-*prob) >> 5
	} else {
		r.rng -= bound
		r.code -= bound
		*prob -= *prob >> 5
		bit = 1
	}
	return bit, nil
}

func (r *rangeDecoder) decodeTree(probs []uint16, bits int) (uint32, error) {
	symbol := uint32(1)
	for i := 0; i < bits; i++ {
		bit, err := r.decodeBit(&probs[symbol])
		if err != nil {
			return 0, err
		}
		symbol = symbol<<1 | bit
	}
	return symbol - (1 << uint(bits)), nil
}

func (r *rangeDecoder) reverseTree(probs []uint16, bits int) (uint32, error) {
	var value uint32
	symbol := uint32(1)
	for i := 0; i < bits; i++ {
		bit, err := r.decodeBit(&probs[symbol])
		if err != nil {
			return 0, err
		}
		symbol = symbol<<1 | bit
		value |= bit << uint(i)
	}
	return value, nil
}

func (r *rangeDecoder) reverseSpecial(probs []uint16, slot, bits int) (uint32, error) {
	base := (2 | (slot & 1)) << uint(bits)
	offset := base - slot - 1
	var value uint32
	symbol := uint32(1)
	for i := 0; i < bits; i++ {
		index := offset + int(symbol)
		if index < 0 || index >= len(probs) {
			return 0, ErrInvalidFormat
		}
		bit, err := r.decodeBit(&probs[index])
		if err != nil {
			return 0, err
		}
		symbol = symbol<<1 | bit
		value |= bit << uint(i)
	}
	return value, nil
}

func (r *rangeDecoder) decodeDirect(bits int) (uint32, error) {
	var value uint32
	for i := bits - 1; i >= 0; i-- {
		if err := r.normalize(); err != nil {
			return 0, err
		}
		r.rng >>= 1
		var bit uint32
		if r.code >= r.rng {
			r.code -= r.rng
			bit = 1
		}
		value |= bit << uint(i)
	}
	return value, nil
}

type rangeEncoder struct {
	low       uint64
	rng       uint32
	cache     byte
	cacheSize uint64
	out       bytes.Buffer
}

func newRangeEncoder() *rangeEncoder {
	return &rangeEncoder{rng: ^uint32(0), cacheSize: 1}
}

func (r *rangeEncoder) shiftLow() {
	lowHi := uint32(r.low >> 32)
	if lowHi != 0 || uint32(r.low) < 0xff000000 {
		temp := r.cache
		for {
			r.out.WriteByte(temp + byte(lowHi))
			temp = 0xff
			r.cacheSize--
			if r.cacheSize == 0 {
				break
			}
		}
		r.cache = byte(r.low >> 24)
	}
	r.cacheSize++
	r.low = (r.low & 0xffffff) << 8
}

func (r *rangeEncoder) encodeBit(prob *uint16, bit uint32) {
	bound := (r.rng >> probBits) * uint32(*prob)
	if bit == 0 {
		r.rng = bound
		*prob += uint16((1<<probBits)-*prob) >> 5
	} else {
		r.low += uint64(bound)
		r.rng -= bound
		*prob -= *prob >> 5
	}
	for r.rng < 1<<24 {
		r.rng <<= 8
		r.shiftLow()
	}
}

func (r *rangeEncoder) encodeTree(probs []uint16, bits int, value uint32) {
	symbol := uint32(1)
	for mask := uint32(1) << uint(bits-1); mask != 0; mask >>= 1 {
		bit := uint32(0)
		if value&mask != 0 {
			bit = 1
		}
		r.encodeBit(&probs[symbol], bit)
		symbol = symbol<<1 | bit
	}
}

func (r *rangeEncoder) finish() {
	for i := 0; i < 5; i++ {
		r.shiftLow()
	}
}

type lzmaEncoder struct {
	lc, lp, pb int
	state      int
	rep        [4]uint32
	previous   byte
	havePrev   bool

	isMatch    []uint16
	isRep      []uint16
	isRepG0    []uint16
	isRepG1    []uint16
	isRepG2    []uint16
	isRep0Long []uint16
	posSlot    []uint16
	posSpecial []uint16
	posAlign   []uint16
	lenChoice  []uint16
	lenLow     []uint16
	lenMid     []uint16
	lenHigh    []uint16
	repChoice  []uint16
	repLow     []uint16
	repMid     []uint16
	repHigh    []uint16
	literal    []uint16
	rng        *rangeEncoder
}

func newLZMAEncoder(props byte) (*lzmaEncoder, error) {
	v := int(props)
	lc := v % 9
	v /= 9
	lp := v % 5
	pb := v / 5
	if pb > numPosBitsMax || lc > 8 || lp > 4 {
		return nil, ErrInvalidFormat
	}
	e := &lzmaEncoder{
		lc:         lc,
		lp:         lp,
		pb:         pb,
		isMatch:    make([]uint16, numStates*numPosStates),
		isRep:      make([]uint16, numStates),
		isRepG0:    make([]uint16, numStates),
		isRepG1:    make([]uint16, numStates),
		isRepG2:    make([]uint16, numStates),
		isRep0Long: make([]uint16, numStates*numPosStates),
		posSlot:    make([]uint16, numLenToPos*(1<<numPosSlotBits)),
		posSpecial: make([]uint16, numFullDistance-endPosModel),
		posAlign:   make([]uint16, 1<<numAlignBits),
		lenChoice:  make([]uint16, 2),
		lenLow:     make([]uint16, numPosStates*8),
		lenMid:     make([]uint16, numPosStates*8),
		lenHigh:    make([]uint16, 1<<8),
		repChoice:  make([]uint16, 2),
		repLow:     make([]uint16, numPosStates*8),
		repMid:     make([]uint16, numPosStates*8),
		repHigh:    make([]uint16, 1<<8),
		literal:    make([]uint16, (1<<(lc+lp))*0x300),
		rng:        newRangeEncoder(),
	}
	e.resetState()
	return e, nil
}

func (e *lzmaEncoder) resetState() {
	e.state = 0
	e.rep = [4]uint32{}
	initProbabilities(e.isMatch)
	initProbabilities(e.isRep)
	initProbabilities(e.isRepG0)
	initProbabilities(e.isRepG1)
	initProbabilities(e.isRepG2)
	initProbabilities(e.isRep0Long)
	initProbabilities(e.posSlot)
	initProbabilities(e.posSpecial)
	initProbabilities(e.posAlign)
	initProbabilities(e.lenChoice)
	initProbabilities(e.lenLow)
	initProbabilities(e.lenMid)
	initProbabilities(e.lenHigh)
	initProbabilities(e.repChoice)
	initProbabilities(e.repLow)
	initProbabilities(e.repMid)
	initProbabilities(e.repHigh)
	initProbabilities(e.literal)
}

func (e *lzmaEncoder) clone() *lzmaEncoder {
	c := *e
	c.rng = newRangeEncoder()
	c.isMatch = append([]uint16(nil), e.isMatch...)
	c.isRep = append([]uint16(nil), e.isRep...)
	c.isRepG0 = append([]uint16(nil), e.isRepG0...)
	c.isRepG1 = append([]uint16(nil), e.isRepG1...)
	c.isRepG2 = append([]uint16(nil), e.isRepG2...)
	c.isRep0Long = append([]uint16(nil), e.isRep0Long...)
	c.posSlot = append([]uint16(nil), e.posSlot...)
	c.posSpecial = append([]uint16(nil), e.posSpecial...)
	c.posAlign = append([]uint16(nil), e.posAlign...)
	c.lenChoice = append([]uint16(nil), e.lenChoice...)
	c.lenLow = append([]uint16(nil), e.lenLow...)
	c.lenMid = append([]uint16(nil), e.lenMid...)
	c.lenHigh = append([]uint16(nil), e.lenHigh...)
	c.repChoice = append([]uint16(nil), e.repChoice...)
	c.repLow = append([]uint16(nil), e.repLow...)
	c.repMid = append([]uint16(nil), e.repMid...)
	c.repHigh = append([]uint16(nil), e.repHigh...)
	c.literal = append([]uint16(nil), e.literal...)
	return &c
}

func (e *lzmaEncoder) encodeLiteral(position int64, b byte) {
	posState := int(position & int64((1<<e.pb)-1))
	e.rng.encodeBit(&e.isMatch[e.state*numPosStates+posState], 0)
	ctx := int(((position & int64((1<<e.lp)-1)) << e.lc) | int64(e.previous>>uint(8-e.lc)))
	p := e.literal[ctx*0x300 : (ctx+1)*0x300]
	symbol := uint32(1)
	if e.state >= 7 && e.havePrev {
		match := e.previous
		for mask := byte(0x80); mask != 0; mask >>= 1 {
			matchBit := uint32(0)
			if match&mask != 0 {
				matchBit = 1
			}
			bit := uint32(0)
			if b&mask != 0 {
				bit = 1
			}
			e.rng.encodeBit(&p[((1+matchBit)<<8)+symbol], bit)
			symbol = symbol<<1 | bit
			if bit != matchBit {
				for mask >>= 1; mask != 0; mask >>= 1 {
					bit = 0
					if b&mask != 0 {
						bit = 1
					}
					e.rng.encodeBit(&p[symbol], bit)
					symbol = symbol<<1 | bit
				}
				break
			}
			match <<= 1
		}
	} else {
		for mask := byte(0x80); mask != 0; mask >>= 1 {
			bit := uint32(0)
			if b&mask != 0 {
				bit = 1
			}
			e.rng.encodeBit(&p[symbol], bit)
			symbol = symbol<<1 | bit
		}
	}
	e.previous = b
	e.havePrev = true
	if e.state < 4 {
		e.state = 0
	} else if e.state < 10 {
		e.state -= 3
	} else {
		e.state -= 6
	}
}

func (e *lzmaEncoder) encodeLength(length, posState int) {
	value := uint32(length - matchMinLen)
	if value < 8 {
		e.rng.encodeBit(&e.lenChoice[0], 0)
		e.rng.encodeTree(e.lenLow[posState*8:], 3, value)
		return
	}
	e.rng.encodeBit(&e.lenChoice[0], 1)
	if value < 16 {
		e.rng.encodeBit(&e.lenChoice[1], 0)
		e.rng.encodeTree(e.lenMid[posState*8:], 3, value-8)
		return
	}
	e.rng.encodeBit(&e.lenChoice[1], 1)
	e.rng.encodeTree(e.lenHigh, 8, value-16)
}

func (e *lzmaEncoder) encodeMatch(position int64, length int) {
	posState := int(position & int64((1<<e.pb)-1))
	oldState := e.state
	e.rng.encodeBit(&e.isMatch[e.state*numPosStates+posState], 1)
	e.rng.encodeBit(&e.isRep[e.state], 0)
	e.encodeLength(length, posState)
	if oldState < 7 {
		e.state = 7
	} else {
		e.state = 10
	}
	lenToPosState := length - matchMinLen
	if lenToPosState > 3 {
		lenToPosState = 3
	}
	e.rng.encodeTree(e.posSlot[lenToPosState*64:], numPosSlotBits, 0)
	e.rep[3] = e.rep[2]
	e.rep[2] = e.rep[1]
	e.rep[1] = e.rep[0]
	e.rep[0] = 0
}

func (e *lzmaEncoder) encodeData(data []byte, position int64) {
	for i := 0; i < len(data); {
		if e.havePrev && data[i] == e.previous {
			run := 1
			for run < len(data)-i && data[i+run] == data[i] && run < matchMaxLen {
				run++
			}
			if run >= matchMinLen {
				e.encodeMatch(position+int64(i), run)
				for j := 0; j < run; j++ {
					e.previous = data[i+j]
					e.havePrev = true
				}
				i += run
				continue
			}
		}
		e.encodeLiteral(position+int64(i), data[i])
		i++
	}
}

func (e *lzmaEncoder) bytes() []byte {
	e.rng.finish()
	return e.rng.out.Bytes()
}

func encodeLZMA2(r io.Reader, w io.Writer, sum *checksum, maxOutput int64) (int64, int64, error) {
	const props byte = 0x5d
	var current *lzmaEncoder
	var position int64
	var compressedSize int64
	var outputSize int64
	buf := make([]byte, chunkSize)
	first := true
	for {
		n, err := r.Read(buf)
		if n < 0 || n > len(buf) {
			return 0, 0, io.ErrShortBuffer
		}
		if n > 0 {
			if maxOutput >= 0 && int64(n) > maxOutput-outputSize {
				return 0, 0, ErrResourceLimit
			}
			data := buf[:n]
			sum.Write(data)
			outputSize += int64(n)

			reset := current == nil
			candidate, candidateErr := newLZMAEncoder(props)
			if candidateErr != nil {
				return 0, 0, candidateErr
			}
			if !reset {
				candidate = current.clone()
			}
			candidate.encodeData(data, position)
			compressed := candidate.bytes()
			compressedCost := len(compressed) + 5
			if reset {
				compressedCost++
			}
			rawCost := n + 3
			if compressedCost < rawCost && len(compressed) <= 1<<16 {
				control := byte(0x80)
				if reset {
					control = 0xe0
				}
				control |= byte((n - 1) >> 16)
				if err := writeAll(w, []byte{control, byte((n - 1) >> 8), byte(n - 1), byte((len(compressed) - 1) >> 8), byte(len(compressed) - 1)}); err != nil {
					return 0, 0, err
				}
				compressedSize += int64(5)
				if reset {
					if err := writeAll(w, []byte{props}); err != nil {
						return 0, 0, err
					}
					compressedSize++
				}
				if err := writeAll(w, compressed); err != nil {
					return 0, 0, err
				}
				compressedSize += int64(len(compressed))
				current = candidate
			} else {
				control := byte(2)
				if first {
					control = 1
				}
				if err := writeAll(w, []byte{control, byte((n - 1) >> 8), byte(n - 1)}); err != nil {
					return 0, 0, err
				}
				if err := writeAll(w, data); err != nil {
					return 0, 0, err
				}
				compressedSize += int64(n + 3)
				if reset {
					current = nil
				}
				if current != nil {
					current.previous = data[len(data)-1]
					current.havePrev = true
				}
			}
			position += int64(n)
			first = false
		}
		if n == 0 && err == nil {
			return 0, 0, io.ErrNoProgress
		}
		if err != nil {
			if err == io.EOF {
				break
			}
			return 0, 0, err
		}
	}
	if err := writeAll(w, []byte{0}); err != nil {
		return 0, 0, err
	}
	return compressedSize + 1, outputSize, nil
}
