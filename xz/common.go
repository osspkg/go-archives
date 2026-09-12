/*
 *  Copyright (c) 2021-2026 Mikhail Knyazhev <markus621@yandex.ru>. All rights reserved.
 *  Use of this source code is governed by a BSD 3-Clause license that can be found in the LICENSE file.
 */

// Package xz provides a small, pure-Go XZ stream implementation and a
// file-backed API for archives containing one logical member.
package xz

import (
	"errors"
	"io"
)

var (
	ErrArchiveClosed   = errors.New("archive is closed")
	ErrFileNotFound    = errors.New("file not found")
	ErrInvalidFileName = errors.New("invalid file name")
	ErrInvalidFormat   = errors.New("invalid xz file format")
	ErrInvalidWriter   = errors.New("invalid output writer")
	ErrResourceLimit   = errors.New("xz resource limit exceeded")
	ErrUnsupported     = errors.New("unsupported xz feature")
)

const (
	DefaultMaxDictionarySize int64 = 64 << 20
	DefaultMaxOutputSize     int64 = 10_000_000_000
	DefaultDictionarySize    int64 = 8 << 20
)

// Options limits memory and output growth while decoding and selects the
// dictionary size used by the encoder. Zero values select the safe defaults.
type Options struct {
	// MaxDictionarySize is the largest LZMA2 dictionary accepted while
	// decoding. Zero selects DefaultMaxDictionarySize.
	MaxDictionarySize int64
	// MaxOutputSize is the largest uncompressed stream accepted. Zero selects
	// DefaultMaxOutputSize.
	MaxOutputSize int64
	// DictionarySize is the LZMA2 dictionary used by the encoder. Zero selects
	// DefaultDictionarySize.
	DictionarySize int64
}

func (o Options) normalized() (Options, error) {
	if o.MaxDictionarySize == 0 {
		o.MaxDictionarySize = DefaultMaxDictionarySize
	}
	if o.MaxOutputSize == 0 {
		o.MaxOutputSize = DefaultMaxOutputSize
	}
	if o.DictionarySize == 0 {
		o.DictionarySize = DefaultDictionarySize
	}
	if o.MaxDictionarySize < 4096 || o.MaxOutputSize < 0 || o.DictionarySize < 4096 {
		return Options{}, ErrResourceLimit
	}
	if o.DictionarySize > o.MaxDictionarySize {
		return Options{}, ErrResourceLimit
	}
	return o, nil
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
