/*
 *  Copyright (c) 2021-2023 Mikhail Knyazhev <markus621@yandex.ru>. All rights reserved.
 *  Use of this source code is governed by a BSD 3-Clause license that can be found in the LICENSE file.
 */

package ar

import "errors"

var (
	signeture  = []byte("!<arch>\n")
	newline    = []byte("\n")
	zero       = []byte("0")
	whitespace = []byte(" ")[0]
	end        = []byte("`\n")
)

var (
	ErrTooLongValue      = errors.New("too long value")
	ErrUnsupportedValue  = errors.New("unsupported value")
	ErrInvalidParseValue = errors.New("parsing error")
	ErrInvalidFileFormat = errors.New("invalid file format")
	ErrInvalidFileName   = errors.New("invalid file name")
	ErrArchiveClosed     = errors.New("archive is closed")
	ErrFileNotFound      = errors.New("file not found")
	ErrFileExist         = errors.New("file already exist")
)

// maxArchiveFileSize is the largest size representable by the ten-byte AR
// file-size field.
const maxArchiveFileSize int64 = 9999999999
