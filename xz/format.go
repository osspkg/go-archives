/*
 *  Copyright (c) 2021-2026 Mikhail Knyazhev <markus621@yandex.ru>. All rights reserved.
 *  Use of this source code is governed by a BSD 3-Clause license that can be found in the LICENSE file.
 */

package xz

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"hash"
	"hash/crc32"
	"hash/crc64"
	"io"
)

var streamMagic = [...]byte{0xfd, '7', 'z', 'X', 'Z', 0x00}

const (
	streamHeaderSize = 12
	streamFooterSize = 12

	checkNone   byte = 0
	checkCRC32  byte = 1
	checkCRC64  byte = 4
	checkSHA256 byte = 10
)

var crc64Table = crc64.MakeTable(crc64.ECMA)

func checksumSize(check byte) (int, error) {
	switch check {
	case checkNone:
		return 0, nil
	case checkCRC32:
		return 4, nil
	case checkCRC64:
		return 8, nil
	case checkSHA256:
		return 32, nil
	default:
		return 0, ErrUnsupported
	}
}

type checksum struct {
	kind  byte
	crc32 uint32
	crc64 uint64
	sha   hash.Hash
}

func newChecksum(kind byte) (*checksum, error) {
	if _, err := checksumSize(kind); err != nil {
		return nil, err
	}
	c := &checksum{kind: kind}
	if kind == checkSHA256 {
		c.sha = sha256.New()
	}
	return c, nil
}

func (c *checksum) Write(p []byte) {
	switch c.kind {
	case checkCRC32:
		c.crc32 = crc32.Update(c.crc32, crc32.IEEETable, p)
	case checkCRC64:
		c.crc64 = crc64.Update(c.crc64, crc64Table, p)
	case checkSHA256:
		_, _ = c.sha.Write(p)
	}
}

func (c *checksum) Sum() []byte {
	switch c.kind {
	case checkCRC32:
		var out [4]byte
		binary.LittleEndian.PutUint32(out[:], c.crc32)
		return out[:]
	case checkCRC64:
		var out [8]byte
		binary.LittleEndian.PutUint64(out[:], c.crc64)
		return out[:]
	case checkSHA256:
		return c.sha.Sum(nil)
	default:
		return nil
	}
}

type byteReader struct {
	r io.Reader
	b [1]byte
}

func (r *byteReader) ReadByte() (byte, error) {
	if _, err := io.ReadFull(r.r, r.b[:]); err != nil {
		return 0, err
	}
	return r.b[0], nil
}

func readVLI(r interface{ ReadByte() (byte, error) }) (uint64, error) {
	var value uint64
	for i := uint(0); i < 9; i++ {
		b, err := r.ReadByte()
		if err != nil {
			return 0, err
		}
		value |= uint64(b&0x7f) << (7 * i)
		if b&0x80 == 0 {
			if i > 0 && b == 0 {
				return 0, ErrInvalidFormat
			}
			if value > uint64(^uint64(0)>>1) {
				return 0, ErrInvalidFormat
			}
			return value, nil
		}
	}
	return 0, ErrInvalidFormat
}

func encodeVLI(value uint64) ([]byte, error) {
	if value > uint64(^uint64(0)>>1) {
		return nil, ErrInvalidFormat
	}
	var out [9]byte
	n := 0
	for {
		b := byte(value & 0x7f)
		value >>= 7
		if value != 0 {
			b |= 0x80
		}
		out[n] = b
		n++
		if value == 0 {
			return out[:n], nil
		}
	}
}

func streamFlags(check byte) [2]byte {
	return [2]byte{0, check}
}

func makeStreamHeader(check byte) ([]byte, error) {
	if _, err := checksumSize(check); err != nil {
		return nil, err
	}
	flags := streamFlags(check)
	var out [streamHeaderSize]byte
	copy(out[:6], streamMagic[:])
	out[6], out[7] = flags[0], flags[1]
	binary.LittleEndian.PutUint32(out[8:], crc32.ChecksumIEEE(out[6:8]))
	return out[:], nil
}

func makeStreamFooter(check byte, indexSize int64) ([]byte, error) {
	if indexSize < 4 || indexSize%4 != 0 {
		return nil, ErrInvalidFormat
	}
	if _, err := checksumSize(check); err != nil {
		return nil, err
	}
	backward := uint64(indexSize/4 - 1)
	if backward > uint64(^uint32(0)) {
		return nil, ErrInvalidFormat
	}
	flags := streamFlags(check)
	var body [6]byte
	binary.LittleEndian.PutUint32(body[:4], uint32(backward))
	body[4], body[5] = flags[0], flags[1]
	var out [streamFooterSize]byte
	binary.LittleEndian.PutUint32(out[:4], crc32.ChecksumIEEE(body[:]))
	copy(out[4:10], body[:])
	out[10], out[11] = 'Y', 'Z'
	return out[:], nil
}

func parseStreamHeader(in []byte) (byte, error) {
	if len(in) != streamHeaderSize || !bytes.Equal(in[:6], streamMagic[:]) {
		return 0, ErrInvalidFormat
	}
	if crc32.ChecksumIEEE(in[6:8]) != binary.LittleEndian.Uint32(in[8:12]) {
		return 0, ErrInvalidFormat
	}
	if in[6] != 0 || in[7]&0xf0 != 0 {
		return 0, ErrUnsupported
	}
	if _, err := checksumSize(in[7] & 0x0f); err != nil {
		return 0, err
	}
	return in[7] & 0x0f, nil
}

func parseStreamFooter(in []byte, check byte) (int64, error) {
	if len(in) != streamFooterSize || in[10] != 'Y' || in[11] != 'Z' {
		return 0, ErrInvalidFormat
	}
	if crc32.ChecksumIEEE(in[4:10]) != binary.LittleEndian.Uint32(in[:4]) {
		return 0, ErrInvalidFormat
	}
	if in[8] != 0 || in[9]&0xf0 != 0 || in[9]&0x0f != check {
		return 0, ErrInvalidFormat
	}
	backward := (uint64(binary.LittleEndian.Uint32(in[4:8])) + 1) * 4
	if backward > uint64(^uint64(0)>>1) {
		return 0, ErrInvalidFormat
	}
	return int64(backward), nil
}

type sliceByteReader struct {
	b []byte
	i int
}

func (r *sliceByteReader) ReadByte() (byte, error) {
	if r.i == len(r.b) {
		return 0, io.EOF
	}
	b := r.b[r.i]
	r.i++
	return b, nil
}

func dictionarySizeFromProperty(prop byte) (int64, error) {
	if prop > 40 {
		return 0, ErrInvalidFormat
	}
	if prop == 40 {
		return 1 << 32, nil
	}
	base := int64(2 | int64(prop&1))
	shift := int(prop/2) + 11
	return base << uint(shift), nil
}

func dictionaryProperty(size int64) (byte, error) {
	for prop := byte(0); prop <= 40; prop++ {
		decoded, err := dictionarySizeFromProperty(prop)
		if err == nil && decoded == size {
			return prop, nil
		}
	}
	return 0, ErrResourceLimit
}

func makeBlockHeader(dictionarySize int64) ([]byte, error) {
	prop, err := dictionaryProperty(dictionarySize)
	if err != nil {
		return nil, err
	}
	// Header size, flags, LZMA2 filter ID, property size, property, padding.
	body := []byte{2, 0, 0x21, 1, prop, 0, 0, 0}
	var crc [4]byte
	binary.LittleEndian.PutUint32(crc[:], crc32.ChecksumIEEE(body))
	return append(body, crc[:]...), nil
}

func parseBlockHeader(in *byteReader, first byte, options Options) (blockInfo, error) {
	if first == 0 {
		return blockInfo{}, ErrInvalidFormat
	}
	headerSize := (int(first) + 1) * 4
	if headerSize < 8 || headerSize > 1024 {
		return blockInfo{}, ErrInvalidFormat
	}
	data := make([]byte, headerSize)
	data[0] = first
	if _, err := io.ReadFull(in.r, data[1:]); err != nil {
		return blockInfo{}, err
	}
	if crc32.ChecksumIEEE(data[:headerSize-4]) != binary.LittleEndian.Uint32(data[headerSize-4:]) {
		return blockInfo{}, ErrInvalidFormat
	}
	flags := data[1]
	if flags&0x3c != 0 {
		return blockInfo{}, ErrUnsupported
	}
	if flags&0x03 != 0 {
		return blockInfo{}, ErrUnsupported
	}
	fields := &sliceByteReader{b: data[2 : headerSize-4]}
	info := blockInfo{headerSize: int64(headerSize), compressedSize: -1, uncompressedSize: -1}
	if flags&0x40 != 0 {
		value, err := readVLI(fields)
		if err != nil {
			return blockInfo{}, err
		}
		if value == 0 {
			return blockInfo{}, ErrInvalidFormat
		}
		info.compressedSize = int64(value)
	}
	if flags&0x80 != 0 {
		value, err := readVLI(fields)
		if err != nil {
			return blockInfo{}, err
		}
		info.uncompressedSize = int64(value)
	}
	filterID, err := readVLI(fields)
	if err != nil || filterID != 0x21 {
		return blockInfo{}, ErrUnsupported
	}
	propertySize, err := readVLI(fields)
	if err != nil || propertySize != 1 {
		return blockInfo{}, ErrUnsupported
	}
	property, err := fields.ReadByte()
	if err != nil {
		return blockInfo{}, err
	}
	dictSize, err := dictionarySizeFromProperty(property)
	if err != nil {
		return blockInfo{}, err
	}
	if dictSize > options.MaxDictionarySize {
		return blockInfo{}, ErrResourceLimit
	}
	info.dictionarySize = dictSize
	for fields.i < len(fields.b) {
		b, err := fields.ReadByte()
		if err != nil {
			return blockInfo{}, err
		}
		if b != 0 {
			return blockInfo{}, ErrInvalidFormat
		}
	}
	return info, nil
}

func makeIndex(records []indexRecord) ([]byte, error) {
	if len(records) == 0 {
		return nil, ErrInvalidFormat
	}
	var body bytes.Buffer
	body.WriteByte(0)
	count, err := encodeVLI(uint64(len(records)))
	if err != nil {
		return nil, err
	}
	body.Write(count)
	for _, record := range records {
		if record.unpaddedSize <= 0 || record.uncompressedSize < 0 {
			return nil, ErrInvalidFormat
		}
		unpadded, err := encodeVLI(uint64(record.unpaddedSize))
		if err != nil {
			return nil, err
		}
		uncompressed, err := encodeVLI(uint64(record.uncompressedSize))
		if err != nil {
			return nil, err
		}
		body.Write(unpadded)
		body.Write(uncompressed)
	}
	for (body.Len()+4)%4 != 0 {
		body.WriteByte(0)
	}
	crc := make([]byte, 4)
	binary.LittleEndian.PutUint32(crc, crc32.ChecksumIEEE(body.Bytes()))
	body.Write(crc)
	return body.Bytes(), nil
}

func readVLIRaw(in *byteReader) (uint64, []byte, error) {
	var raw [9]byte
	for i := 0; i < len(raw); i++ {
		b, err := in.ReadByte()
		if err != nil {
			return 0, nil, err
		}
		raw[i] = b
		if b&0x80 == 0 {
			value, err := readVLI(&sliceByteReader{b: raw[:i+1]})
			return value, raw[:i+1], err
		}
	}
	return 0, nil, ErrInvalidFormat
}

func parseIndex(in *byteReader, records []indexRecord) ([]byte, error) {
	var raw bytes.Buffer
	raw.WriteByte(0)
	count, encoded, err := readVLIRaw(in)
	if err != nil {
		return nil, err
	}
	raw.Write(encoded)
	if count != uint64(len(records)) {
		return nil, ErrInvalidFormat
	}
	for i := range records {
		unpadded, encoded, err := readVLIRaw(in)
		if err != nil {
			return nil, err
		}
		raw.Write(encoded)
		uncompressed, encoded, err := readVLIRaw(in)
		if err != nil {
			return nil, err
		}
		raw.Write(encoded)
		if unpadded != uint64(records[i].unpaddedSize) || uncompressed != uint64(records[i].uncompressedSize) {
			return nil, ErrInvalidFormat
		}
	}
	for (raw.Len()+4)%4 != 0 {
		b, err := in.ReadByte()
		if err != nil {
			return nil, err
		}
		if b != 0 {
			return nil, ErrInvalidFormat
		}
		raw.WriteByte(b)
	}
	var crc [4]byte
	if _, err := io.ReadFull(in.r, crc[:]); err != nil {
		return nil, err
	}
	raw.Write(crc[:])
	if crc32.ChecksumIEEE(raw.Bytes()[:raw.Len()-4]) != binary.LittleEndian.Uint32(crc[:]) {
		return nil, ErrInvalidFormat
	}
	return raw.Bytes(), nil
}
