/*
 *  Copyright (c) 2021-2026 Mikhail Knyazhev <markus621@yandex.ru>. All rights reserved.
 *  Use of this source code is governed by a BSD 3-Clause license that can be found in the LICENSE file.
 */

package zst

import (
	"encoding/binary"
	"io"
	"math/bits"

	codec "github.com/klauspost/compress/zstd"
)

const (
	standardMagic        uint32 = 0xfd2fb528
	skippableMagicBase   uint32 = 0x184d2a50
	maxEncoderWindowSize        = 1 << 29
)

func encodeStream(src io.Reader, dst io.Writer, options Options) error {
	opts, _, err := options.normalized()
	if err != nil {
		return err
	}
	for i, payload := range opts.SkippableFrames {
		if err := writeSkippable(dst, uint32(i), payload); err != nil {
			return err
		}
	}

	buf := make([]byte, int(opts.FrameSize))
	var total int64
	frames := 0
	for {
		n, readErr := io.ReadFull(src, buf)
		switch readErr {
		case nil:
			if int64(n) > opts.MaxOutputSize-total {
				return ErrResourceLimit
			}
			if err := encodeFrame(buf[:n], dst, opts); err != nil {
				return err
			}
			frames++
			total += int64(n)
		case io.ErrUnexpectedEOF:
			if n == 0 {
				if frames == 0 {
					if err := encodeFrame(nil, dst, opts); err != nil {
						return err
					}
				}
				return nil
			}
			if int64(n) > opts.MaxOutputSize-total {
				return ErrResourceLimit
			}
			if err := encodeFrame(buf[:n], dst, opts); err != nil {
				return err
			}
			return nil
		case io.EOF:
			if frames == 0 {
				if err := encodeFrame(nil, dst, opts); err != nil {
					return err
				}
			}
			return nil
		default:
			return readErr
		}
	}
}

func writeSkippable(dst io.Writer, index uint32, payload []byte) error {
	var header [8]byte
	binary.LittleEndian.PutUint32(header[0:4], skippableMagicBase+(index&15))
	binary.LittleEndian.PutUint32(header[4:8], uint32(len(payload)))
	if err := writeAll(dst, header[:]); err != nil {
		return err
	}
	return writeAll(dst, payload)
}

func encodeFrame(data []byte, dst io.Writer, options Options) error {
	_, readerOptions, err := options.normalized()
	if err != nil {
		return err
	}
	windowSize := encoderWindowSize(options.WindowSize)
	if windowSize == 0 {
		return ErrResourceLimit
	}

	encoderOptions := []codec.EOption{
		codec.WithEncoderConcurrency(1),
		codec.WithEncoderCRC(true),
		codec.WithEncoderLevel(codec.EncoderLevelFromZstd(options.CompressionLevel)),
		codec.WithWindowSize(windowSize),
		codec.WithSingleSegment(int64(len(data)) <= options.WindowSize),
		codec.WithZeroFrames(true),
	}
	if len(readerOptions.dictionary) != 0 {
		encoderOptions = append(encoderOptions, codec.WithEncoderDict(readerOptions.dictionary))
	}
	encoder, err := codec.NewWriter(nil, encoderOptions...)
	if err != nil {
		return err
	}
	encoded := encoder.EncodeAll(data, nil)
	if len(encoded) == 0 {
		return ErrInvalidFormat
	}
	return writeAll(dst, encoded)
}

func encoderWindowSize(size int64) int {
	if size < 1<<10 || size > maxEncoderWindowSize {
		return 0
	}
	u := uint64(size)
	p := uint64(1) << uint(bits.Len64(u-1))
	if p > maxEncoderWindowSize {
		return 0
	}
	return int(p)
}
