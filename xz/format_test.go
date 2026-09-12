/*
 *  Copyright (c) 2021-2023 Mikhail Knyazhev <markus621@yandex.ru>. All rights reserved.
 *  Use of this source code is governed by a BSD 3-Clause license that can be found in the LICENSE file.
 */

package xz

import (
	"bytes"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"io"
	"testing"
)

func TestVLIRoundTrip(t *testing.T) {
	values := []uint64{0, 1, 127, 128, 255, 1 << 14, 1 << 32, uint64(^uint64(0) >> 1)}
	for _, want := range values {
		encoded, err := encodeVLI(want)
		if err != nil {
			t.Fatalf("encode %d: %v", want, err)
		}
		got, err := readVLI(&sliceByteReader{b: encoded})
		if err != nil {
			t.Fatalf("decode %d (%x): %v", want, encoded, err)
		}
		if got != want {
			t.Fatalf("decode %x: got %d, want %d", encoded, got, want)
		}
	}
}

func TestVLIRejectsNonCanonicalAndOverflow(t *testing.T) {
	invalid := [][]byte{
		{0x80, 0x00},
		{0x80, 0x80, 0x00},
		{0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80},
	}
	for _, input := range invalid {
		_, err := readVLI(&sliceByteReader{b: input})
		if !errors.Is(err, ErrInvalidFormat) {
			t.Errorf("decode %x: got %v, want ErrInvalidFormat", input, err)
		}
	}
	if _, err := encodeVLI(uint64(^uint64(0)>>1) + 1); !errors.Is(err, ErrInvalidFormat) {
		t.Fatalf("encode overflow: got %v", err)
	}
}

func TestBlockSizeFields(t *testing.T) {
	options, err := (Options{}).normalized()
	if err != nil {
		t.Fatal(err)
	}
	data := bytes.Repeat([]byte("block sizes "), 1000)
	var original bytes.Buffer
	if _, err := encodeStream(bytes.NewReader(data), &original, options); err != nil {
		t.Fatal(err)
	}
	raw := original.Bytes()
	property := raw[16]
	compressedStart := 24
	compressedInput := &countedByteReader{r: &byteReader{r: bytes.NewReader(raw[compressedStart:])}}
	decoded := &decodeOutput{dst: io.Discard, buf: make([]byte, 0, 32<<10), max: options.MaxOutputSize}
	compressedSize, err := decodeLZMA2(compressedInput, decoded, options.DictionarySize)
	if err != nil {
		t.Fatal(err)
	}
	checkStart := compressedStart + int(compressedSize)
	padding := int((4 - (12+compressedSize+8)%4) % 4)
	checkStart += padding
	check := raw[checkStart : checkStart+8]

	compressedVLI, _ := encodeVLI(uint64(compressedSize))
	uncompressedVLI, _ := encodeVLI(uint64(len(data)))
	blockBody := []byte{3, 0xc0}
	blockBody = append(blockBody, compressedVLI...)
	blockBody = append(blockBody, uncompressedVLI...)
	blockBody = append(blockBody, 0x21, 1, property)
	for (len(blockBody)+4)%4 != 0 {
		blockBody = append(blockBody, 0)
	}
	blockCRC := make([]byte, 4)
	binary.LittleEndian.PutUint32(blockCRC, crc32.ChecksumIEEE(blockBody))
	blockHeader := append(blockBody, blockCRC...)
	unpaddedSize := int64(len(blockHeader)) + compressedSize + int64(len(check))
	index, err := makeIndex([]indexRecord{{unpaddedSize: unpaddedSize, uncompressedSize: int64(len(data))}})
	if err != nil {
		t.Fatal(err)
	}
	footer, err := makeStreamFooter(checkCRC64, int64(len(index)))
	if err != nil {
		t.Fatal(err)
	}
	var withSizes bytes.Buffer
	withSizes.Write(raw[:12])
	withSizes.Write(blockHeader)
	withSizes.Write(raw[compressedStart : compressedStart+int(compressedSize)])
	for i := int64(0); i < (4-unpaddedSize%4)%4; i++ {
		withSizes.WriteByte(0)
	}
	withSizes.Write(check)
	withSizes.Write(index)
	withSizes.Write(footer)

	var got bytes.Buffer
	if _, err := decodeStream(bytes.NewReader(withSizes.Bytes()), &got, options); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got.Bytes(), data) {
		t.Fatalf("decoded data differs: got %d bytes, want %d", got.Len(), len(data))
	}
}
