/*
 *  Copyright (c) 2021-2026 Mikhail Knyazhev <markus621@yandex.ru>. All rights reserved.
 *  Use of this source code is governed by a BSD 3-Clause license that can be found in the LICENSE file.
 */

package zst

import (
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

// Header describes the only logical member of a Zstandard archive.
type Header struct {
	FileName string
	Size     int64
}

// Arch is a file-backed Zstandard archive containing one logical member.
type Arch struct {
	fd      *os.File
	path    string
	member  string
	header  Header
	options Options
	mux     sync.RWMutex
}

// Open opens or creates a Zstandard archive with default options.
func Open(filename string, perm os.FileMode) (*Arch, error) {
	return OpenWithOptions(filename, perm, Options{})
}

// OpenWithOptions opens or creates a Zstandard archive.
func OpenWithOptions(filename string, perm os.FileMode, options Options) (*Arch, error) {
	normalized, _, err := options.normalized()
	if err != nil {
		return nil, err
	}
	member, err := memberName(filename)
	if err != nil {
		return nil, err
	}
	archivePath, err := filepath.Abs(filename)
	if err != nil {
		return nil, fmt.Errorf("resolve zstd archive path: %w", err)
	}
	file, err := os.OpenFile(archivePath, os.O_RDWR|os.O_CREATE, perm)
	if errors.Is(err, os.ErrPermission) {
		file, err = os.Open(archivePath)
	}
	if err != nil {
		return nil, fmt.Errorf("open zstd archive: %w", err)
	}
	stat, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("stat zstd archive: %w", err)
	}
	if !stat.Mode().IsRegular() {
		_ = file.Close()
		return nil, fmt.Errorf("zstd archive is not a regular file: %s", filename)
	}

	a := &Arch{fd: file, path: archivePath, member: member, options: normalized}
	if err := a.load(); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("read zstd archive: %w", err)
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
		if _, err := encodeStream(bytes.NewReader(nil), a.fd, a.options); err != nil {
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

// Read streams the logical member to w.
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
	input, err := fileSection(a.fd)
	if err != nil {
		return err
	}
	size, err := decodeStream(input, w, a.options)
	if err != nil {
		return err
	}
	if size != a.header.Size {
		return ErrInvalidFormat
	}
	return nil
}

// Write atomically replaces the stream with data. perm applies to the ZST file.
func (a *Arch) Write(filename string, data []byte, perm fs.FileMode) error {
	a.mux.Lock()
	defer a.mux.Unlock()
	if a.fd == nil {
		return ErrArchiveClosed
	}
	if err := a.checkMember(filename); err != nil {
		return err
	}
	if int64(len(data)) > a.options.MaxOutputSize {
		return ErrResourceLimit
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
	temp, err := os.CreateTemp(canonicalDir, ".go-archives-zst-")
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
	temp, err := os.CreateTemp(canonicalDir, ".go-archives-zst-")
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
	if _, err := encodeStream(src, temp, a.options); err != nil {
		return err
	}
	if err := temp.Sync(); err != nil {
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	tempClosed = true

	validated, err := os.Open(tempName)
	if err != nil {
		return err
	}
	validatedClosed := false
	defer func() {
		if !validatedClosed {
			if closeErr := validated.Close(); retErr == nil && closeErr != nil {
				retErr = closeErr
			}
		}
	}()
	size, err := decodeStreamFromFile(validated, a.options)
	if err != nil {
		return err
	}
	if err := os.Rename(tempName, target); err != nil {
		return err
	}
	removeTemp = false
	validatedClosed = true
	oldFile := a.fd
	a.fd = validated
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

func decodeStreamFromFile(file *os.File, options Options) (int64, error) {
	input, err := fileSection(file)
	if err != nil {
		return 0, err
	}
	return decodeStream(input, io.Discard, options)
}

func decodeStream(src io.Reader, dst io.Writer, options Options) (int64, error) {
	_, readerOptions, err := options.normalized()
	if err != nil {
		return 0, err
	}
	reader, err := newReader(src, readerOptions)
	if err != nil {
		return 0, err
	}
	defer reader.close()
	counted := &countingWriter{w: dst}
	if _, err := io.Copy(counted, reader); err != nil {
		return 0, err
	}
	return counted.n, nil
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
	if err != nil {
		return n, err
	}
	if n != len(p) {
		return n, io.ErrShortWrite
	}
	return n, nil
}

func memberName(filename string) (string, error) {
	base := filepath.Base(filename)
	if base == "." || base == string(filepath.Separator) || base == "" || strings.ContainsAny(base, `/\\`) {
		return "", ErrInvalidFileName
	}
	base = strings.TrimSuffix(base, ".zst")
	if err := validateExportName(base); err != nil {
		return "", err
	}
	return base, nil
}

func validateExportName(name string) error {
	if name == "" || name == "." || name == ".." || filepath.IsAbs(name) ||
		strings.ContainsAny(name, `/\\`) || strings.IndexByte(name, 0) >= 0 {
		return ErrInvalidFileName
	}
	return nil
}
