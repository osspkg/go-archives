/*
 *  Copyright (c) 2021-2026 Mikhail Knyazhev <markus621@yandex.ru>. All rights reserved.
 *  Use of this source code is governed by a BSD 3-Clause license that can be found in the LICENSE file.
 */

// Package zst provides a file-backed archive API for one logical member using
// the pure-Go Zstandard codec from github.com/klauspost/compress/zstd.
package zst

import (
	"encoding/binary"
	"errors"
	"io"
)

var (
	ErrArchiveClosed   = errors.New("archive is closed")
	ErrFileNotFound    = errors.New("file not found")
	ErrInvalidFileName = errors.New("invalid file name")
	ErrInvalidFormat   = errors.New("invalid zstd file format")
	ErrInvalidWriter   = errors.New("invalid output writer")
	ErrResourceLimit   = errors.New("zstd resource limit exceeded")
	ErrUnsupported     = errors.New("unsupported zstd feature")
)

const (
	DefaultCompressionLevel = 3
	DefaultFrameSize        = int64(8 << 20)
	DefaultBlockSize        = int64(128 << 10)
	DefaultWindowSize       = int64(64 << 20)
	DefaultMaxWindowSize    = int64(64 << 20)
	DefaultMaxOutputSize    = int64(10_000_000_000)
	DefaultMaxSkippableSize = int64(16 << 20)
)

// Options controls compression and resource limits. Zero values select the
// documented defaults.
type Options struct {
	// CompressionLevel is mapped to the compression levels exposed by the
	// Zstandard dependency. Zero selects level 3.
	CompressionLevel int
	// FrameSize is the maximum uncompressed data stored in one frame.
	FrameSize int64
	// BlockSize is retained for API compatibility. The external codec chooses
	// its block size.
	BlockSize int64
	// WindowSize is the maximum back-reference distance.
	WindowSize int64

	// MaxWindowSize limits windows accepted by the decoder.
	MaxWindowSize int64
	// MaxOutputSize limits the total decompressed output.
	MaxOutputSize int64
	// MaxSkippableSize limits metadata frames accepted by the decoder.
	MaxSkippableSize int64

	// Dictionary may contain an official formatted Zstandard dictionary. Raw
	// dictionaries are accepted for compatibility when DictionaryID is set,
	// but are not consumed by the external codec.
	Dictionary   []byte
	DictionaryID uint32

	// SkippableFrames are emitted before the standard data frames. Their
	// payload is metadata and is ignored by the decoder.
	SkippableFrames [][]byte
}

type readerOptions struct {
	maxWindowSize    uint64
	maxOutputSize    uint64
	maxSkippableSize uint64
	dictionary       []byte
}

func (o Options) normalized() (Options, readerOptions, error) {
	if o.CompressionLevel == 0 {
		o.CompressionLevel = DefaultCompressionLevel
	}
	if o.FrameSize == 0 {
		o.FrameSize = DefaultFrameSize
	}
	if o.BlockSize == 0 {
		o.BlockSize = DefaultBlockSize
	}
	if o.WindowSize == 0 {
		o.WindowSize = DefaultWindowSize
	}
	if o.MaxWindowSize == 0 {
		o.MaxWindowSize = DefaultMaxWindowSize
	}
	if o.MaxOutputSize == 0 {
		o.MaxOutputSize = DefaultMaxOutputSize
	}
	if o.MaxSkippableSize == 0 {
		o.MaxSkippableSize = DefaultMaxSkippableSize
	}
	if o.FrameSize > o.MaxOutputSize {
		o.FrameSize = o.MaxOutputSize
	}
	if o.CompressionLevel < 1 || o.CompressionLevel > 22 ||
		o.FrameSize < 1 || o.BlockSize < 1 || o.BlockSize > DefaultBlockSize ||
		o.WindowSize < 1 || o.MaxWindowSize < 1<<10 || o.MaxOutputSize < 0 ||
		o.MaxSkippableSize < 0 || o.WindowSize > o.MaxWindowSize {
		return Options{}, readerOptions{}, ErrResourceLimit
	}
	if o.MaxWindowSize > int64(^uint32(0))<<10 {
		return Options{}, readerOptions{}, ErrResourceLimit
	}
	maxInt := int64(^uint(0) >> 1)
	if o.FrameSize > maxInt || o.BlockSize > maxInt || o.WindowSize > maxInt {
		return Options{}, readerOptions{}, ErrResourceLimit
	}
	for _, payload := range o.SkippableFrames {
		if int64(len(payload)) > o.MaxSkippableSize || uint64(len(payload)) > uint64(^uint32(0)) {
			return Options{}, readerOptions{}, ErrResourceLimit
		}
	}
	if int64(len(o.Dictionary)) > o.MaxWindowSize {
		return Options{}, readerOptions{}, ErrResourceLimit
	}

	formattedDictionary := isFormattedDictionary(o.Dictionary)
	if len(o.Dictionary) != 0 && len(o.Dictionary) < 8 {
		return Options{}, readerOptions{}, ErrInvalidFormat
	}
	if o.DictionaryID != 0 && len(o.Dictionary) == 0 {
		return Options{}, readerOptions{}, ErrInvalidFormat
	}
	if formattedDictionary {
		id := binary.LittleEndian.Uint32(o.Dictionary[4:8])
		if id == 0 || (o.DictionaryID != 0 && o.DictionaryID != id) {
			return Options{}, readerOptions{}, ErrInvalidFormat
		}
	} else if len(o.Dictionary) != 0 && o.DictionaryID == 0 {
		return Options{}, readerOptions{}, ErrUnsupported
	}
	// klauspost/compress accepts the official formatted dictionary only. Raw
	// dictionaries remain accepted for compatibility, but are not referenced
	// in generated frames because the dependency cannot consume them.
	dictionary := o.Dictionary
	if !formattedDictionary {
		dictionary = nil
	}
	ro := readerOptions{
		maxWindowSize:    uint64(o.MaxWindowSize),
		maxOutputSize:    uint64(o.MaxOutputSize),
		maxSkippableSize: uint64(o.MaxSkippableSize),
		dictionary:       dictionary,
	}
	return o, ro, nil
}

func writeAll(w io.Writer, p []byte) error {
	for len(p) > 0 {
		n, err := w.Write(p)
		if n < 0 || n > len(p) {
			return io.ErrShortWrite
		}
		p = p[n:]
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
	}
	return nil
}
