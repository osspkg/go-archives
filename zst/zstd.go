/*
 *  Copyright (c) 2021-2026 Mikhail Knyazhev <markus621@yandex.ru>. All rights reserved.
 *  Use of this source code is governed by a BSD 3-Clause license that can be found in the LICENSE file.
 */

package zst

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	codec "github.com/klauspost/compress/zstd"
)

// Reader streams decompressed data from a concatenation of Zstandard frames.
// The frame codec and entropy decoders are provided by the external
// github.com/klauspost/compress/zstd package.
type Reader struct {
	decoder *codec.Decoder
	scanner *frameScanner
	options readerOptions
	pending error
	total   uint64
}

// NewReader creates a Reader with the default safety limits.
func NewReader(input io.Reader) *Reader {
	_, options, _ := (Options{}).normalized()
	r, err := newReader(input, options)
	if err != nil {
		return &Reader{options: options, pending: err}
	}
	return r
}

// NewReaderWithOptions creates a Reader with the supplied safety limits and
// optional formatted dictionary.
func NewReaderWithOptions(input io.Reader, options Options) (*Reader, error) {
	_, readerOptions, err := options.normalized()
	if err != nil {
		return nil, err
	}
	r, err := newReader(input, readerOptions)
	if err != nil {
		return nil, err
	}
	return r, nil
}

func newReader(input io.Reader, options readerOptions) (*Reader, error) {
	r := &Reader{options: options}
	if err := r.reset(input); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *Reader) reset(input io.Reader) error {
	r.close()
	r.pending = nil
	r.total = 0
	if input == nil {
		input = invalidReader{}
	}

	framed := &frameScanner{
		r:            input,
		maxSkippable: r.options.maxSkippableSize,
		state:        scanMagic,
	}
	r.scanner = framed
	decoderOptions := []codec.DOption{
		codec.WithDecoderConcurrency(1),
		codec.WithDecoderLowmem(true),
		codec.WithDecoderMaxMemory(decoderMemoryLimit(r.options)),
		codec.WithDecoderMaxWindow(r.options.maxWindowSize),
	}
	if len(r.options.dictionary) != 0 {
		decoderOptions = append(decoderOptions, codec.WithDecoderDicts(r.options.dictionary))
	}
	decoder, err := codec.NewReader(framed, decoderOptions...)
	if err != nil {
		r.pending = translateCodecError(err)
		return r.pending
	}
	r.decoder = decoder
	return nil
}

// Reset discards the current decoder state and starts reading input again.
func (r *Reader) Reset(input io.Reader) {
	if err := r.reset(input); err != nil {
		r.pending = err
	}
}

// Read implements io.Reader.
func (r *Reader) Read(p []byte) (int, error) {
	if r.pending != nil {
		return 0, r.pending
	}
	if r.decoder == nil {
		return 0, ErrInvalidFormat
	}
	if len(p) == 0 {
		return 0, nil
	}
	if r.total >= r.options.maxOutputSize {
		var probe [1]byte
		n, err := r.decoder.Read(probe[:])
		if r.scanner != nil && r.scanner.err != nil {
			return 0, translateCodecError(r.scanner.err)
		}
		if n != 0 {
			return 0, ErrResourceLimit
		}
		if err == nil {
			return 0, io.ErrNoProgress
		}
		return 0, translateCodecError(err)
	}
	remaining := r.options.maxOutputSize - r.total
	if uint64(len(p)) > remaining {
		p = p[:int(remaining)]
	}
	n, err := r.decoder.Read(p)
	if n < 0 || n > len(p) {
		return 0, ErrInvalidFormat
	}
	r.total += uint64(n)
	if r.scanner != nil && r.scanner.err != nil {
		return n, translateCodecError(r.scanner.err)
	}
	if n == 0 && err == nil {
		return 0, io.ErrNoProgress
	}
	if errors.Is(err, io.EOF) && !r.scanner.sawDataFrame {
		return n, io.ErrUnexpectedEOF
	}
	return n, translateCodecError(err)
}

// ReadByte implements io.ByteReader.
func (r *Reader) ReadByte() (byte, error) {
	var b [1]byte
	n, err := r.Read(b[:])
	if n == 1 {
		return b[0], err
	}
	return 0, err
}

func (r *Reader) close() {
	if r.decoder != nil {
		r.decoder.Close()
		r.decoder = nil
	}
}

func translateCodecError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrResourceLimit) {
		return err
	}
	switch {
	case errors.Is(err, codec.ErrDecoderSizeExceeded),
		errors.Is(err, codec.ErrWindowSizeExceeded),
		errors.Is(err, codec.ErrFrameSizeExceeded):
		return fmt.Errorf("%w: %s", ErrResourceLimit, err.Error())
	case errors.Is(err, codec.ErrMagicMismatch),
		errors.Is(err, codec.ErrReservedBlockType),
		errors.Is(err, codec.ErrCompressedSizeTooBig),
		errors.Is(err, codec.ErrBlockTooSmall),
		errors.Is(err, codec.ErrUnexpectedBlockSize),
		errors.Is(err, codec.ErrUnknownDictionary),
		errors.Is(err, codec.ErrFrameSizeMismatch),
		errors.Is(err, codec.ErrCRCMismatch):
		return fmt.Errorf("%w: %s", ErrInvalidFormat, err.Error())
	default:
		return err
	}
}

func decoderMemoryLimit(options readerOptions) uint64 {
	limit := options.maxOutputSize
	if limit < options.maxWindowSize {
		limit = options.maxWindowSize
	}
	if limit < 1<<10 {
		limit = 1 << 10
	}
	return limit
}

type invalidReader struct{}

func (invalidReader) Read([]byte) (int, error) { return 0, ErrInvalidFormat }

type scanState uint8

const (
	scanMagic scanState = iota
	scanDescriptor
	scanHeader
	scanBlockHeader
	scanBlockPayload
	scanChecksum
	scanSkippableSize
	scanSkippablePayload
)

// frameScanner validates only enough of the frame structure to enforce the
// skippable-frame limit without buffering the compressed stream. Full frame,
// block and entropy validation remains the responsibility of the dependency.
type frameScanner struct {
	r              io.Reader
	maxSkippable   uint64
	err            error
	state          scanState
	magic          [4]byte
	magicN         int
	headerRemain   int
	hasChecksum    bool
	blockHeader    [3]byte
	blockHeaderN   int
	blockRemain    uint64
	blockLast      bool
	checksumRemain int
	skipSize       [4]byte
	skipSizeN      int
	sawDataFrame   bool
}

func (s *frameScanner) Read(p []byte) (int, error) {
	if s.err != nil {
		return 0, s.err
	}
	if len(p) == 0 {
		return 0, nil
	}
	n, err := s.r.Read(p)
	if n < 0 || n > len(p) {
		return 0, io.ErrShortBuffer
	}
	for i := 0; i < n; i++ {
		if scanErr := s.consume(p[i]); scanErr != nil {
			s.err = scanErr
			return i + 1, scanErr
		}
	}
	if err != nil {
		if errors.Is(err, io.EOF) {
			if s.state != scanMagic || !s.sawDataFrame {
				return n, io.ErrUnexpectedEOF
			}
		}
		return n, err
	}
	return n, nil
}

func (s *frameScanner) consume(b byte) error {
	switch s.state {
	case scanMagic:
		return s.consumeMagic(b)
	case scanDescriptor:
		return s.consumeDescriptor(b)
	case scanHeader:
		return s.consumeHeader()
	case scanBlockHeader:
		return s.consumeBlockHeader(b)
	case scanBlockPayload:
		return s.consumeBlockPayload()
	case scanChecksum:
		return s.consumeChecksum()
	case scanSkippableSize:
		return s.consumeSkippableSize(b)
	case scanSkippablePayload:
		return s.consumeSkippablePayload()
	default:
		return ErrInvalidFormat
	}
}

func (s *frameScanner) consumeMagic(b byte) error {
	s.magic[s.magicN] = b
	s.magicN++
	if s.magicN != len(s.magic) {
		return nil
	}
	s.magicN = 0
	magic := binary.LittleEndian.Uint32(s.magic[:])
	switch {
	case magic == standardMagic:
		s.sawDataFrame = true
		s.state = scanDescriptor
	case magic >= skippableMagicBase && magic <= skippableMagicBase+15:
		s.state = scanSkippableSize
		s.skipSizeN = 0
	default:
		return ErrInvalidFormat
	}
	return nil
}

func (s *frameScanner) consumeDescriptor(b byte) error {
	if b&(1<<3) != 0 {
		return ErrInvalidFormat
	}
	s.hasChecksum = b&(1<<2) != 0
	s.headerRemain = 0
	if b&(1<<5) == 0 {
		s.headerRemain++
	}
	s.headerRemain += singleSegmentHeaderSize(b & 3)
	fcsSize := 1 << (b >> 6)
	if fcsSize == 1 && b&(1<<5) == 0 {
		fcsSize = 0
	}
	s.headerRemain += fcsSize
	if s.headerRemain == 0 {
		s.state = scanBlockHeader
	} else {
		s.state = scanHeader
	}
	return nil
}

func singleSegmentHeaderSize(flag byte) int {
	switch flag {
	case 1:
		return 1
	case 2:
		return 2
	case 3:
		return 4
	default:
		return 0
	}
}

func (s *frameScanner) consumeHeader() error {
	s.headerRemain--
	if s.headerRemain == 0 {
		s.state = scanBlockHeader
	}
	return nil
}

func (s *frameScanner) consumeBlockHeader(b byte) error {
	s.blockHeader[s.blockHeaderN] = b
	s.blockHeaderN++
	if s.blockHeaderN != len(s.blockHeader) {
		return nil
	}
	s.blockHeaderN = 0
	header := uint32(s.blockHeader[0]) |
		uint32(s.blockHeader[1])<<8 |
		uint32(s.blockHeader[2])<<16
	blockType := (header >> 1) & 3
	if blockType == 3 {
		return ErrInvalidFormat
	}
	s.blockLast = header&1 != 0
	s.blockRemain = uint64(header >> 3)
	if blockType == 1 {
		s.blockRemain = 1
	}
	if s.blockRemain == 0 {
		return s.finishBlock()
	}
	s.state = scanBlockPayload
	return nil
}

func (s *frameScanner) consumeBlockPayload() error {
	s.blockRemain--
	if s.blockRemain == 0 {
		return s.finishBlock()
	}
	return nil
}

func (s *frameScanner) consumeChecksum() error {
	s.checksumRemain--
	if s.checksumRemain == 0 {
		s.state = scanMagic
	}
	return nil
}

func (s *frameScanner) consumeSkippableSize(b byte) error {
	s.skipSize[s.skipSizeN] = b
	s.skipSizeN++
	if s.skipSizeN != len(s.skipSize) {
		return nil
	}
	size := uint64(binary.LittleEndian.Uint32(s.skipSize[:]))
	if size > s.maxSkippable {
		return ErrResourceLimit
	}
	s.blockRemain = size
	if s.blockRemain == 0 {
		s.state = scanMagic
	} else {
		s.state = scanSkippablePayload
	}
	return nil
}

func (s *frameScanner) consumeSkippablePayload() error {
	s.blockRemain--
	if s.blockRemain == 0 {
		s.state = scanMagic
	}
	return nil
}

func (s *frameScanner) finishBlock() error {
	if !s.blockLast {
		s.state = scanBlockHeader
		return nil
	}
	if s.hasChecksum {
		s.checksumRemain = 4
		s.state = scanChecksum
	} else {
		s.state = scanMagic
	}
	return nil
}
