/*
 *  Copyright (c) 2021-2026 Mikhail Knyazhev <markus621@yandex.ru>. All rights reserved.
 *  Use of this source code is governed by a BSD 3-Clause license that can be found in the LICENSE file.
 */

package zst

import "encoding/binary"

const dictionaryMagic uint32 = 0xec30a437

func isFormattedDictionary(data []byte) bool {
	return len(data) >= 4 && binary.LittleEndian.Uint32(data[:4]) == dictionaryMagic
}
