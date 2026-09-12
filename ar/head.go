/*
 *  Copyright (c) 2021-2023 Mikhail Knyazhev <markus621@yandex.ru>. All rights reserved.
 *  Use of this source code is governed by a BSD 3-Clause license that can be found in the LICENSE file.
 */

package ar

import (
	"bytes"
	"reflect"
	"strconv"
	"strings"
)

type kind struct {
	From, Len, Base int
	Prefix          string
	Type            string
}

var (
	fileName = kind{From: 0, Len: 16, Type: "string"}
	modif    = kind{From: 16, Len: 12, Base: 10, Type: "int"}
	ownerID  = kind{From: 28, Len: 6, Base: 10, Type: "int"}
	groupID  = kind{From: 34, Len: 6, Base: 10, Type: "int"}
	fileMode = kind{From: 40, Len: 8, Base: 8, Prefix: "100", Type: "int"}
	fileSize = kind{From: 48, Len: 10, Base: 10, Type: "int"}
	endChar  = kind{From: 58, Len: 2, Type: "nil"}
)

type buffer []byte

func newBuffer(size int) buffer {
	v := make(buffer, size)
	for i := 0; i < size; i++ {
		v[i] = whitespace
	}
	return v
}

func (v buffer) Write(k kind, d interface{}) error {
	if k.From < 0 || k.Len < 0 || k.From > len(v) || k.Len > len(v)-k.From {
		return ErrInvalidParseValue
	}

	var data []byte

	switch vv := d.(type) {
	case []byte:
		data = vv
	case string:
		data = []byte(k.Prefix + vv)
	case int64:
		data = []byte(k.Prefix + strconv.FormatInt(vv, k.Base))
	default:
		return ErrUnsupportedValue
	}

	if len(data) > k.Len {
		return ErrTooLongValue
	}
	copy(v[k.From:k.From+k.Len], data)
	return nil
}

func (v buffer) Read(k kind, d interface{}) error {
	rv := reflect.ValueOf(d)
	if rv.Kind() != reflect.Ptr || rv.IsNil() {
		return ErrInvalidParseValue
	}
	if k.From < 0 || k.Len < 0 || k.From > len(v) || k.Len > len(v)-k.From {
		return ErrInvalidParseValue
	}

	data := strings.TrimRight(string(v[k.From:k.From+k.Len]), " /")

	if len(k.Prefix) > 0 {
		data = strings.TrimPrefix(data, k.Prefix)
	}

	switch k.Type {
	case "string":
		if rv.Elem().Kind() != reflect.String || !rv.Elem().CanSet() {
			return ErrInvalidParseValue
		}
		rv.Elem().SetString(data)
		return nil
	case "int":
		if rv.Elem().Kind() < reflect.Int || rv.Elem().Kind() > reflect.Int64 || !rv.Elem().CanSet() {
			return ErrInvalidParseValue
		}
		i, err := strconv.ParseInt(data, k.Base, 64)
		if err != nil {
			return err
		}
		rv.Elem().SetInt(i)
		return nil
	}

	return ErrInvalidParseValue
}

//////////////////////////////////////////////////////////////////////////////////////////////////////////////////

// HEAD_SIZE default size for meta data of contained files
const HEAD_SIZE int = 60

// Header meta data of contained files
type Header struct {
	FileName  string
	Timestamp int64
	Mode      int64
	Size      int64
}

// Bytes make string from Header model
func (v *Header) Bytes() ([]byte, error) {
	if v == nil {
		return nil, ErrInvalidParseValue
	}
	if v.Size < 0 || v.Size > maxArchiveFileSize || v.Mode < 0 {
		return nil, ErrInvalidParseValue
	}

	data := newBuffer(HEAD_SIZE)

	list := map[kind]interface{}{
		fileName: v.FileName,
		modif:    v.Timestamp,
		ownerID:  zero,
		groupID:  zero,
		fileMode: v.Mode,
		fileSize: v.Size,
		endChar:  end,
	}

	for k, val := range list {
		if err := data.Write(k, val); err != nil {
			return nil, err
		}
	}

	return []byte(data), nil
}

// Parse decode string to Header model
func (v *Header) Parse(b []byte) error {
	if v == nil || len(b) != HEAD_SIZE {
		return ErrInvalidParseValue
	}
	if !bytes.Equal(b[endChar.From:endChar.From+endChar.Len], end) {
		return ErrInvalidFileFormat
	}

	vv := buffer(b)
	var owner, group int64

	list := []func() error{
		func() error { return vv.Read(fileName, &v.FileName) },
		func() error { return vv.Read(modif, &v.Timestamp) },
		func() error { return vv.Read(ownerID, &owner) },
		func() error { return vv.Read(groupID, &group) },
		func() error { return vv.Read(fileMode, &v.Mode) },
		func() error { return vv.Read(fileSize, &v.Size) },
	}

	for _, fn := range list {
		if err := fn(); err != nil {
			return err
		}
	}
	if v.Mode < 0 || v.Size < 0 || v.Size > maxArchiveFileSize {
		return ErrInvalidFileFormat
	}

	return nil
}
