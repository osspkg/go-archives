/*
 *  Copyright (c) 2021-2026 Mikhail Knyazhev <markus621@yandex.ru>. All rights reserved.
 *  Use of this source code is governed by a BSD 3-Clause license that can be found in the LICENSE file.
 */

package ar

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
	"time"
)

type Arch struct {
	fd      *os.File
	headers []Header
	files   map[string]position
	mux     sync.RWMutex
}

type position struct {
	From int64
	Len  int64
}

func Open(filename string, perm os.FileMode) (*Arch, error) {
	file, err := os.OpenFile(filename, os.O_RDWR|os.O_SYNC|os.O_CREATE|os.O_APPEND, perm)
	if err != nil {
		return nil, fmt.Errorf("open archive: %w", err)
	}

	v := &Arch{fd: file, headers: make([]Header, 0), files: make(map[string]position)}

	if err := v.rwSignature(); err != nil {
		if closeErr := file.Close(); closeErr != nil {
			return nil, fmt.Errorf("write archive signature: %w (close archive: %s)", err, closeErr.Error())
		}
		return nil, fmt.Errorf("write archive signature: %w", err)
	}

	if err := v.readAllHeaders(); err != nil {
		if closeErr := file.Close(); closeErr != nil {
			return nil, fmt.Errorf("read archive: %w (close archive: %s)", err, closeErr.Error())
		}
		return nil, fmt.Errorf("read archive: %w", err)
	}

	return v, nil
}

func (v *Arch) Close() error {
	v.mux.Lock()
	defer v.mux.Unlock()

	if v.fd == nil {
		return nil
	}
	err := v.fd.Close()
	v.fd = nil
	return err
}

func (v *Arch) List() []Header {
	v.mux.RLock()
	defer v.mux.RUnlock()

	nh := make([]Header, 0, len(v.headers))
	nh = append(nh, v.headers...)
	return nh
}

func (v *Arch) Read(filename string, w io.Writer) error {
	v.mux.RLock()
	defer v.mux.RUnlock()
	if v.fd == nil {
		return ErrArchiveClosed
	}
	if w == nil {
		return ErrInvalidParseValue
	}

	pos, ok := v.files[filename]
	if !ok {
		return fmt.Errorf("%w: %s", ErrFileNotFound, filename)
	}
	buf := make([]byte, 256)
	if pos.From < 0 || pos.Len < 0 {
		return ErrInvalidFileFormat
	}
	remaining := pos.Len
	offset := pos.From
	for remaining > 0 {
		readLen := int64(len(buf))
		if remaining < readLen {
			readLen = remaining
		}

		i, err := v.fd.ReadAt(buf[:readLen], offset)
		if i > 0 {
			written, writeErr := w.Write(buf[:i])
			if writeErr != nil {
				return fmt.Errorf("write content: %w", writeErr)
			}
			if written != i {
				return fmt.Errorf("write content: %w", io.ErrShortWrite)
			}
			offset += int64(i)
			remaining -= int64(i)
		}
		if err != nil {
			if errors.Is(err, io.EOF) && remaining == 0 {
				return nil
			}
			return fmt.Errorf("read content: %w", err)
		}
		if i == 0 {
			return fmt.Errorf("read content: %w", io.ErrUnexpectedEOF)
		}
	}
	return nil
}

func (v *Arch) Write(filename string, b []byte, perm fs.FileMode) error {
	v.mux.Lock()
	defer v.mux.Unlock()
	if v.fd == nil {
		return ErrArchiveClosed
	}

	if _, ok := v.files[filename]; ok {
		return fmt.Errorf("%w: %s", ErrFileExist, filename)
	}
	cur, err := v.fd.Seek(0, io.SeekEnd)
	if err != nil {
		return err
	}

	h := &Header{
		FileName:  filename,
		Timestamp: time.Now().Unix(),
		Mode:      int64(perm),
		Size:      int64(len(b)),
	}
	hb, err := h.Bytes()
	if err != nil {
		return err
	}

	if err := v.writeRecord(cur, hb, h.Size%2 != 0, func() error {
		return writeAll(v.fd, b)
	}); err != nil {
		return err
	}

	cur += int64(HEAD_SIZE)
	v.files[filename] = position{From: cur, Len: h.Size}
	v.headers = append(v.headers, *h)

	return nil
}

func (v *Arch) Export(filename, dir string) (retErr error) {
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
	file, err := os.CreateTemp(canonicalDir, ".go-archives-")
	if err != nil {
		return err
	}
	removeTemp := true
	fileClosed := false
	defer func() {
		if !fileClosed {
			if closeErr := file.Close(); retErr == nil && closeErr != nil {
				retErr = closeErr
			}
		}
		if removeTemp {
			if removeErr := os.Remove(file.Name()); retErr == nil && removeErr != nil {
				retErr = removeErr
			}
		}
	}()

	if err = v.Read(filename, file); err != nil {
		return err
	}
	if err = file.Close(); err != nil {
		fileClosed = true
		return err
	}
	fileClosed = true
	if err = os.Rename(file.Name(), target); err != nil {
		return err
	}
	removeTemp = false
	return nil
}

func (v *Arch) Import(filename string, perm fs.FileMode) (retErr error) {
	v.mux.Lock()
	defer v.mux.Unlock()
	if v.fd == nil {
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
	archiveStat, err := v.fd.Stat()
	if err != nil {
		return err
	}
	if os.SameFile(stat, archiveStat) {
		return fmt.Errorf("import source is the archive: %s", filename)
	}
	if stat.Size() < 0 || stat.Size() > maxArchiveFileSize {
		return fmt.Errorf("import source is too large: %s", filename)
	}
	if _, ok := v.files[stat.Name()]; ok {
		return fmt.Errorf("%w: %s", ErrFileExist, stat.Name())
	}
	cur, err := v.fd.Seek(0, io.SeekEnd)
	if err != nil {
		return err
	}

	if perm == 0 {
		perm = stat.Mode().Perm()
	}

	h := &Header{
		FileName:  stat.Name(),
		Timestamp: stat.ModTime().Unix(),
		Mode:      int64(perm),
		Size:      stat.Size(),
	}
	hb, err := h.Bytes()
	if err != nil {
		return err
	}
	if err = v.writeRecord(cur, hb, h.Size%2 != 0, func() error {
		_, copyErr := io.CopyN(v.fd, file, h.Size)
		return copyErr
	}); err != nil {
		return err
	}

	cur += int64(HEAD_SIZE)
	v.files[stat.Name()] = position{From: cur, Len: h.Size}
	v.headers = append(v.headers, *h)

	return nil
}

func (v *Arch) correctSize(size int64, callFunc func()) {
	if size%2 != 0 {
		callFunc()
	}
}

func (v *Arch) writeRecord(start int64, header []byte, addPadding bool, writeBody func() error) error {
	writeErr := func(err error) error {
		if rollbackErr := v.rollback(start); rollbackErr != nil {
			return fmt.Errorf("%w (rollback failed: %s)", err, rollbackErr.Error())
		}
		return err
	}

	if err := writeAll(v.fd, header); err != nil {
		return writeErr(err)
	}
	if err := writeBody(); err != nil {
		return writeErr(err)
	}
	if addPadding {
		if err := writeAll(v.fd, newline); err != nil {
			return writeErr(err)
		}
	}
	return nil
}

func (v *Arch) rollback(start int64) error {
	if err := v.fd.Truncate(start); err != nil {
		return err
	}
	_, err := v.fd.Seek(start, io.SeekStart)
	return err
}

func writeAll(w io.Writer, data []byte) error {
	for len(data) > 0 {
		n, err := w.Write(data)
		if n < 0 || n > len(data) {
			return io.ErrShortWrite
		}
		data = data[n:]
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
	}
	return nil
}

func validateExportName(filename string) error {
	if filename == "" || filename == "." || filename == ".." || filepath.IsAbs(filename) ||
		filepath.Base(filename) != filename || strings.ContainsAny(filename, `/\`) {
		return fmt.Errorf("%w: %s", ErrInvalidFileName, filename)
	}
	return nil
}

func (v *Arch) rwSignature() error {
	data := make([]byte, len(signeture))
	i, err := io.ReadFull(v.fd, data)
	if i == 0 && errors.Is(err, io.EOF) {
		return writeAll(v.fd, signeture)
	}
	if err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return ErrInvalidFileFormat
		}
		return err
	}
	if !bytes.Equal(signeture, data) {
		return ErrInvalidFileFormat
	}

	return nil
}

func (v *Arch) readAllHeaders() error {
	stat, err := v.fd.Stat()
	if err != nil {
		return err
	}
	archiveSize := stat.Size()
	data := make([]byte, HEAD_SIZE)
	for {
		n, err := io.ReadFull(v.fd, data)
		if errors.Is(err, io.EOF) && n == 0 {
			return nil
		}
		if err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				return fmt.Errorf("%w: truncated header", ErrInvalidFileFormat)
			}
			return err
		}

		head := &Header{}
		if err = head.Parse(data); err != nil {
			return err
		}

		cur, err := v.fd.Seek(0, io.SeekCurrent)
		if err != nil {
			return err
		}

		seek := head.Size
		v.correctSize(seek, func() { seek++ })
		if cur < 0 || cur > archiveSize || seek < 0 || seek > archiveSize-cur {
			return fmt.Errorf("%w: member exceeds archive", ErrInvalidFileFormat)
		}

		v.files[head.FileName] = position{From: cur, Len: head.Size}
		v.headers = append(v.headers, *head)

		if _, err := v.fd.Seek(seek, io.SeekCurrent); err != nil {
			return err
		}
	}
}
