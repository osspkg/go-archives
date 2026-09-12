/*
 *  Copyright (c) 2021-2026 Mikhail Knyazhev <markus621@yandex.ru>. All rights reserved.
 *  Use of this source code is governed by a BSD 3-Clause license that can be found in the LICENSE file.
 */

package xz

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Header describes the only logical member of an XZ archive. XZ does not
// store filesystem permissions or timestamps; Size is the uncompressed size.
type Header struct {
	FileName string
	Size     int64
}

// Arch is a file-backed XZ archive containing one logical member.
type Arch struct {
	fd      *os.File
	path    string
	member  string
	header  Header
	options Options
	mux     sync.RWMutex
}

type blockInfo struct {
	headerSize       int64
	compressedSize   int64
	uncompressedSize int64
	dictionarySize   int64
}

type indexRecord struct {
	unpaddedSize     int64
	uncompressedSize int64
}

// Open opens or creates a single-stream XZ archive with default limits.
func Open(filename string, perm os.FileMode) (*Arch, error) {
	return OpenWithOptions(filename, perm, Options{})
}

// OpenWithOptions opens or creates a single-stream XZ archive.
func OpenWithOptions(filename string, perm os.FileMode, options Options) (*Arch, error) {
	options, err := options.normalized()
	if err != nil {
		return nil, err
	}
	member, err := memberName(filename)
	if err != nil {
		return nil, err
	}
	archivePath, err := filepath.Abs(filename)
	if err != nil {
		return nil, fmt.Errorf("resolve xz archive path: %w", err)
	}
	file, err := os.OpenFile(archivePath, os.O_RDWR|os.O_CREATE, perm)
	if errors.Is(err, os.ErrPermission) {
		file, err = os.Open(archivePath)
	}
	if err != nil {
		return nil, fmt.Errorf("open xz archive: %w", err)
	}
	stat, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("stat xz archive: %w", err)
	}
	if !stat.Mode().IsRegular() {
		_ = file.Close()
		return nil, fmt.Errorf("xz archive is not a regular file: %s", filename)
	}

	a := &Arch{fd: file, path: archivePath, member: member, options: options}
	if err := a.load(); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("read xz archive: %w", err)
	}
	return a, nil
}

func (a *Arch) load() error {
	stat, err := a.fd.Stat()
	if err != nil {
		return err
	}
	if stat.Size() == 0 {
		if _, err := a.fd.Seek(0, io.SeekStart); err != nil {
			return err
		}
		if err := encodeStream(bytes.NewReader(nil), a.fd, a.options); err != nil {
			return err
		}
		if err := a.fd.Sync(); err != nil {
			return err
		}
		stat, err = a.fd.Stat()
		if err != nil {
			return err
		}
	}
	if _, err := a.fd.Seek(0, io.SeekStart); err != nil {
		return err
	}
	size, err := decodeStream(io.NewSectionReader(a.fd, 0, stat.Size()), io.Discard, a.options)
	if err != nil {
		return err
	}
	if _, err := a.fd.Seek(0, io.SeekStart); err != nil {
		return err
	}
	a.header = Header{FileName: a.member, Size: size}
	return nil
}

// Close releases the archive file. It is safe to call repeatedly.
func (a *Arch) Close() error {
	a.mux.Lock()
	defer a.mux.Unlock()
	if a.fd == nil {
		return nil
	}
	err := a.fd.Close()
	a.fd = nil
	return err
}

// List returns the archive's one logical member, or nil after Close.
func (a *Arch) List() []Header {
	a.mux.RLock()
	defer a.mux.RUnlock()
	if a.fd == nil {
		return nil
	}
	return []Header{a.header}
}

// Read streams the only logical member to w.
func (a *Arch) Read(filename string, w io.Writer) error {
	a.mux.RLock()
	defer a.mux.RUnlock()
	if a.fd == nil {
		return ErrArchiveClosed
	}
	if w == nil {
		return ErrInvalidWriter
	}
	if err := a.checkMember(filename); err != nil {
		return err
	}
	stat, err := a.fd.Stat()
	if err != nil {
		return err
	}
	size, err := decodeStream(io.NewSectionReader(a.fd, 0, stat.Size()), w, a.options)
	if err != nil {
		return err
	}
	if size != a.header.Size {
		return ErrInvalidFormat
	}
	return nil
}

// Write atomically replaces the stream with data. perm applies to the XZ file.
func (a *Arch) Write(filename string, data []byte, perm fs.FileMode) error {
	a.mux.Lock()
	defer a.mux.Unlock()
	if a.fd == nil {
		return ErrArchiveClosed
	}
	if err := a.checkMember(filename); err != nil {
		return err
	}
	return a.replace(bytes.NewReader(data), perm)
}

// Import atomically replaces the stream with the contents of a regular file.
func (a *Arch) Import(filename string, perm fs.FileMode) (retErr error) {
	a.mux.Lock()
	defer a.mux.Unlock()
	if a.fd == nil {
		return ErrArchiveClosed
	}
	file, err := os.Open(filename)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := file.Close(); retErr == nil && closeErr != nil {
			retErr = closeErr
		}
	}()
	stat, err := file.Stat()
	if err != nil {
		return err
	}
	if !stat.Mode().IsRegular() {
		return fmt.Errorf("import source is not a regular file: %s", filename)
	}
	archiveStat, err := a.fd.Stat()
	if err != nil {
		return err
	}
	if os.SameFile(stat, archiveStat) {
		return fmt.Errorf("import source is the archive: %s", filename)
	}
	if err := a.checkMember(filepath.Base(filename)); err != nil {
		return err
	}
	if stat.Size() < 0 || stat.Size() > a.options.MaxOutputSize {
		return ErrResourceLimit
	}
	if perm == 0 {
		perm = stat.Mode().Perm()
	}
	return a.replace(file, perm)
}

// Export streams the member into dir using a temporary file and rename.
func (a *Arch) Export(filename, dir string) (retErr error) {
	a.mux.RLock()
	defer a.mux.RUnlock()
	if a.fd == nil {
		return ErrArchiveClosed
	}
	if err := a.checkMember(filename); err != nil {
		return err
	}
	if err := validateExportName(filename); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	canonicalDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return err
	}
	target := filepath.Join(canonicalDir, filename)
	temp, err := os.CreateTemp(canonicalDir, ".go-archives-xz-")
	if err != nil {
		return err
	}
	tempName := temp.Name()
	removeTemp := true
	tempClosed := false
	defer func() {
		if !tempClosed {
			if closeErr := temp.Close(); retErr == nil && closeErr != nil {
				retErr = closeErr
			}
		}
		if removeTemp {
			if removeErr := os.Remove(tempName); retErr == nil && removeErr != nil {
				retErr = removeErr
			}
		}
	}()
	input, err := fileSection(a.fd)
	if err != nil {
		return err
	}
	if _, err := decodeStream(input, temp, a.options); err != nil {
		return err
	}
	if err := temp.Chmod(0600); err != nil {
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	tempClosed = true
	if err := os.Rename(tempName, target); err != nil {
		return err
	}
	removeTemp = false
	return nil
}

func (a *Arch) checkMember(filename string) error {
	if err := validateExportName(filename); err != nil {
		return err
	}
	if filename != a.member {
		return fmt.Errorf("%w: %s", ErrFileNotFound, filename)
	}
	return nil
}

func (a *Arch) replace(src io.Reader, perm fs.FileMode) (retErr error) {
	dir := filepath.Dir(a.path)
	canonicalDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return err
	}
	target := filepath.Join(canonicalDir, filepath.Base(a.path))
	mode := fs.FileMode(perm).Perm()
	if mode == 0 {
		stat, statErr := a.fd.Stat()
		if statErr != nil {
			return statErr
		}
		mode = stat.Mode().Perm()
	}
	temp, err := os.CreateTemp(canonicalDir, ".go-archives-xz-")
	if err != nil {
		return err
	}
	tempName := temp.Name()
	removeTemp := true
	tempClosed := false
	defer func() {
		if !tempClosed {
			if closeErr := temp.Close(); retErr == nil && closeErr != nil {
				retErr = closeErr
			}
		}
		if removeTemp {
			if removeErr := os.Remove(tempName); retErr == nil && removeErr != nil {
				retErr = removeErr
			}
		}
	}()
	if err := temp.Chmod(mode); err != nil {
		return err
	}
	if err := encodeStream(src, temp, a.options); err != nil {
		return err
	}
	if err := temp.Sync(); err != nil {
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	tempClosed = true
	if err := os.Rename(tempName, target); err != nil {
		return err
	}
	removeTemp = false

	newFile, err := os.Open(target)
	if err != nil {
		return err
	}
	input, err := fileSection(newFile)
	if err != nil {
		_ = newFile.Close()
		return err
	}
	size, err := decodeStream(input, io.Discard, a.options)
	if err != nil {
		_ = newFile.Close()
		return err
	}
	if _, err := newFile.Seek(0, io.SeekStart); err != nil {
		_ = newFile.Close()
		return err
	}
	oldFile := a.fd
	a.fd = newFile
	a.header.Size = size
	return oldFile.Close()
}

func fileSection(file *os.File) (io.Reader, error) {
	stat, err := file.Stat()
	if err != nil {
		return nil, err
	}
	return io.NewSectionReader(file, 0, stat.Size()), nil
}

func memberName(filename string) (string, error) {
	base := filepath.Base(filename)
	if base == "." || base == string(filepath.Separator) || base == "" || strings.ContainsAny(base, `/\\`) {
		return "", ErrInvalidFileName
	}
	base = strings.TrimSuffix(base, ".xz")
	if err := validateExportName(base); err != nil {
		return "", err
	}
	return base, nil
}

func validateExportName(name string) error {
	if name == "" || name == "." || name == ".." || filepath.IsAbs(name) || strings.ContainsAny(name, `/\\`) {
		return ErrInvalidFileName
	}
	return nil
}

func encodeStream(src io.Reader, dst io.Writer, options Options) error {
	var err error
	options, err = options.normalized()
	if err != nil {
		return err
	}
	header, err := makeStreamHeader(checkCRC64)
	if err != nil {
		return err
	}
	if err := writeAll(dst, header); err != nil {
		return err
	}

	blockHeader, err := makeBlockHeader(options.DictionarySize)
	if err != nil {
		return err
	}
	if err := writeAll(dst, blockHeader); err != nil {
		return err
	}
	sum, err := newChecksum(checkCRC64)
	if err != nil {
		return err
	}
	counting := &countingWriter{w: dst}
	compressedData, uncompressed, err := encodeLZMA2(src, counting, sum, options.MaxOutputSize)
	if err != nil {
		return err
	}
	if compressedData != counting.n {
		return ErrInvalidFormat
	}
	check := sum.Sum()
	unpadded := int64(len(blockHeader)) + compressedData + int64(len(check))
	padding := int((4 - unpadded%4) % 4)
	if err := writeAll(dst, make([]byte, padding)); err != nil {
		return err
	}
	if err := writeAll(dst, check); err != nil {
		return err
	}

	index, err := makeIndex([]indexRecord{{unpaddedSize: unpadded, uncompressedSize: uncompressed}})
	if err != nil {
		return err
	}
	if err := writeAll(dst, index); err != nil {
		return err
	}
	footer, err := makeStreamFooter(checkCRC64, int64(len(index)))
	if err != nil {
		return err
	}
	if err := writeAll(dst, footer); err != nil {
		return err
	}
	return nil
}

func decodeStream(src io.Reader, dst io.Writer, options Options) (int64, error) {
	var err error
	options, err = options.normalized()
	if err != nil {
		return 0, err
	}
	br := bufio.NewReaderSize(src, 32<<10)
	input := &byteReader{r: br}
	check, checkLen, err := readStreamHeader(br)
	if err != nil {
		return 0, err
	}
	output := &decodeOutput{dst: dst, buf: make([]byte, 0, 32<<10), max: options.MaxOutputSize}
	records, err := decodeBlocks(input, br, output, options, check, checkLen)
	if err != nil {
		return 0, err
	}

	index, err := parseIndex(input, records)
	if err != nil {
		return 0, fmt.Errorf("parse index: %w", err)
	}
	var footer [streamFooterSize]byte
	if _, err := io.ReadFull(br, footer[:]); err != nil {
		return 0, fmt.Errorf("read footer: %w", err)
	}
	backward, err := parseStreamFooter(footer[:], check)
	if err != nil {
		return 0, fmt.Errorf("parse footer: %w", err)
	}
	if backward != int64(len(index)) {
		return 0, fmt.Errorf("footer index size %d != %d: %w", backward, len(index), ErrInvalidFormat)
	}
	if err := readStreamPadding(input); err != nil {
		return 0, err
	}
	return output.count, nil
}

func readStreamHeader(br *bufio.Reader) (byte, int, error) {
	var header [streamHeaderSize]byte
	if _, err := io.ReadFull(br, header[:]); err != nil {
		return 0, 0, err
	}
	check, err := parseStreamHeader(header[:])
	if err != nil {
		return 0, 0, fmt.Errorf("parse stream header: %w", err)
	}
	checkLen, err := checksumSize(check)
	if err != nil {
		return 0, 0, err
	}
	return check, checkLen, nil
}

func decodeBlocks(input *byteReader, br *bufio.Reader, output *decodeOutput, options Options, check byte, checkLen int) ([]indexRecord, error) {
	records := make([]indexRecord, 0, 1)
	for {
		first, err := input.ReadByte()
		if err != nil {
			return nil, err
		}
		if first == 0 {
			break
		}
		record, err := decodeBlock(input, br, output, options, first, check, checkLen)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
		if len(records) > 1<<20 {
			return nil, ErrResourceLimit
		}
	}
	return records, nil
}

func decodeBlock(input *byteReader, br *bufio.Reader, output *decodeOutput, options Options, first, check byte, checkLen int) (indexRecord, error) {
	block, err := parseBlockHeader(input, first, options)
	if err != nil {
		return indexRecord{}, fmt.Errorf("parse block header: %w", err)
	}
	sum, err := newChecksum(check)
	if err != nil {
		return indexRecord{}, err
	}
	output.sum = sum
	blockReader := interface{ ReadByte() (byte, error) }(input)
	if block.compressedSize >= 0 {
		blockReader = &limitedByteReader{r: input, remaining: block.compressedSize}
	}
	blockInput := &countedByteReader{r: blockReader}
	before := output.count
	compressedData, err := decodeLZMA2(blockInput, output, block.dictionarySize)
	if err != nil {
		return indexRecord{}, fmt.Errorf("decode lzma2: %w", err)
	}
	if err := output.flush(); err != nil {
		return indexRecord{}, fmt.Errorf("write decoded data: %w", err)
	}
	if compressedData != blockInput.n {
		return indexRecord{}, fmt.Errorf("lzma size mismatch: %w", ErrInvalidFormat)
	}
	actualUncompressed := output.count - before
	if block.uncompressedSize >= 0 && block.uncompressedSize != actualUncompressed {
		return indexRecord{}, fmt.Errorf("block uncompressed size: %w", ErrInvalidFormat)
	}
	if block.compressedSize >= 0 && block.compressedSize != compressedData {
		return indexRecord{}, fmt.Errorf("block compressed size: %w", ErrInvalidFormat)
	}
	unpadded := block.headerSize + compressedData + int64(checkLen)
	if err := readBlockPadding(input, unpadded); err != nil {
		return indexRecord{}, err
	}
	actualCheck := make([]byte, checkLen)
	if _, err := io.ReadFull(br, actualCheck); err != nil {
		return indexRecord{}, fmt.Errorf("read block check: %w", err)
	}
	expectedCheck := sum.Sum()
	if !bytes.Equal(actualCheck, expectedCheck) {
		return indexRecord{}, fmt.Errorf("block check got %x want %x: %w", actualCheck, expectedCheck, ErrInvalidFormat)
	}
	return indexRecord{unpaddedSize: unpadded, uncompressedSize: actualUncompressed}, nil
}

func readBlockPadding(input *byteReader, unpadded int64) error {
	padding := int((4 - unpadded%4) % 4)
	for i := 0; i < padding; i++ {
		b, err := input.ReadByte()
		if err != nil {
			return fmt.Errorf("read block padding: %w", err)
		}
		if b != 0 {
			return fmt.Errorf("block padding: %w", ErrInvalidFormat)
		}
	}
	return nil
}

func readStreamPadding(input *byteReader) error {
	padding := 0
	for {
		b, err := input.ReadByte()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return err
		}
		if b != 0 {
			return ErrUnsupported
		}
		padding++
		if padding > 1<<20 {
			return ErrResourceLimit
		}
	}
	if padding%4 != 0 {
		return ErrInvalidFormat
	}
	return nil
}

type countingWriter struct {
	w io.Writer
	n int64
}

func (w *countingWriter) Write(p []byte) (int, error) {
	n, err := w.w.Write(p)
	if n < 0 || n > len(p) {
		return 0, io.ErrShortWrite
	}
	w.n += int64(n)
	if err == nil && n != len(p) {
		return n, io.ErrShortWrite
	}
	return n, err
}
