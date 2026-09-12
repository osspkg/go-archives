package zst

import "encoding/binary"

const dictionaryMagic uint32 = 0xec30a437

func isFormattedDictionary(data []byte) bool {
	return len(data) >= 4 && binary.LittleEndian.Uint32(data[:4]) == dictionaryMagic
}
