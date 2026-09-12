/*
 *  Copyright (c) 2021-2023 Mikhail Knyazhev <markus621@yandex.ru>. All rights reserved.
 *  Use of this source code is governed by a BSD 3-Clause license that can be found in the LICENSE file.
 */

package ar_test

import (
	"testing"

	"go.osspkg.com/archives/ar"
)

func TestUnit_NewBuffer(t *testing.T) {
	demo := []byte("hello.go        123456      0     0     100777  999       `\n")
	h := &ar.Header{
		FileName:  "hello.go",
		Timestamp: 123456,
		Size:      999,
		Mode:      0777,
	}
	b, err := h.Bytes()
	mustNoError(t, err)
	mustEqual(t, demo, b)

	h2 := &ar.Header{}
	mustNoError(t, h2.Parse(demo))

	mustEqual(t, h, h2)
}

func TestUnit_HeaderRejectsInvalidInput(t *testing.T) {
	for _, data := range [][]byte{nil, []byte("short")} {
		h := &ar.Header{}
		mustNotPanics(t, func() {
			mustError(t, h.Parse(data))
		})
	}

	h := &ar.Header{Size: -1}
	_, err := h.Bytes()
	mustError(t, err)

	valid, err := (&ar.Header{}).Bytes()
	mustNoError(t, err)
	valid[ar.HEAD_SIZE-2] = 'x'
	mustError(t, (&ar.Header{}).Parse(valid))
}
