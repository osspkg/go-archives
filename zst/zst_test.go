package zst

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestStreamRoundTrip(t *testing.T) {
	data := bytes.Repeat([]byte("zstandard data "), 10000)
	var compressed bytes.Buffer
	if _, err := encodeStream(bytes.NewReader(data), &compressed, Options{FrameSize: 1 << 16}); err != nil {
		t.Fatal(err)
	}
	if compressed.Len() >= len(data) {
		t.Fatalf("repeated data was not compressed: %d >= %d", compressed.Len(), len(data))
	}
	reader := NewReader(bytes.NewReader(compressed.Bytes()))
	var decoded bytes.Buffer
	if _, err := io.Copy(&decoded, reader); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(decoded.Bytes(), data) {
		t.Fatal("round-trip mismatch")
	}
}

func TestStreamSkippableAndRLE(t *testing.T) {
	data := bytes.Repeat([]byte{'x'}, 100000)
	var compressed bytes.Buffer
	if _, err := encodeStream(bytes.NewReader(data), &compressed, Options{
		FrameSize:       1 << 16,
		SkippableFrames: [][]byte{[]byte("metadata")},
	}); err != nil {
		t.Fatal(err)
	}
	reader := NewReader(bytes.NewReader(compressed.Bytes()))
	decoded, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(decoded, data) {
		t.Fatal("skippable/RLE mismatch")
	}
}

func TestRawDictionary(t *testing.T) {
	dictionary := []byte("dictionary prefix for zstandard tests")
	data := []byte("dictionary-aware payload")
	var compressed bytes.Buffer
	options := Options{Dictionary: dictionary, DictionaryID: 40000}
	if _, err := encodeStream(bytes.NewReader(data), &compressed, options); err != nil {
		t.Fatal(err)
	}
	reader, err := NewReaderWithOptions(bytes.NewReader(compressed.Bytes()), options)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(decoded, data) {
		t.Fatal("dictionary round-trip mismatch")
	}
}

func TestConfiguredWindowSize(t *testing.T) {
	data := bytes.Repeat([]byte("window "), 1000)
	options := Options{FrameSize: int64(len(data)), WindowSize: 1024, BlockSize: 1024}
	var compressed bytes.Buffer
	if _, err := encodeStream(bytes.NewReader(data), &compressed, options); err != nil {
		t.Fatal(err)
	}
	decoded, err := io.ReadAll(NewReader(bytes.NewReader(compressed.Bytes())))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(decoded, data) {
		t.Fatal("window size round-trip mismatch")
	}
}

func TestSmallBlockSize(t *testing.T) {
	data := []byte("small block size")
	var compressed bytes.Buffer
	if _, err := encodeStream(bytes.NewReader(data), &compressed, Options{BlockSize: 1}); err != nil {
		t.Fatal(err)
	}
	decoded, err := io.ReadAll(NewReader(bytes.NewReader(compressed.Bytes())))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(decoded, data) {
		t.Fatal("small block mismatch")
	}
}

func TestLargeCompressedBlock(t *testing.T) {
	data := bytes.Repeat([]byte("large compressed block with repeated words "), 30000)
	var compressed bytes.Buffer
	if _, err := encodeStream(bytes.NewReader(data), &compressed, Options{FrameSize: int64(len(data))}); err != nil {
		t.Fatal(err)
	}
	decoded, err := io.ReadAll(NewReader(bytes.NewReader(compressed.Bytes())))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(decoded, data) {
		t.Fatal("large block mismatch")
	}
}

func TestExternalZstdCompatibility(t *testing.T) {
	if _, err := exec.LookPath("zstd"); err != nil {
		t.Skip("zstd is not installed")
	}
	data := bytes.Repeat([]byte("external compatibility "), 5000)
	var compressed bytes.Buffer
	if _, err := encodeStream(bytes.NewReader(data), &compressed, Options{FrameSize: 1 << 15}); err != nil {
		t.Fatal(err)
	}
	decode := exec.Command("zstd", "-q", "-d", "-c")
	decode.Stdin = bytes.NewReader(compressed.Bytes())
	decoded, err := decode.Output()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(decoded, data) {
		t.Fatal("external decoder mismatch")
	}

	encode := exec.Command("zstd", "-q", "-c")
	encode.Stdin = bytes.NewReader(data)
	foreign, err := encode.Output()
	if err != nil {
		t.Fatal(err)
	}
	reader := NewReader(bytes.NewReader(foreign))
	decoded, err = io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(decoded, data) {
		t.Fatal("foreign decoder mismatch")
	}
}

func TestFormattedDictionaryCompatibility(t *testing.T) {
	if _, err := exec.LookPath("zstd"); err != nil {
		t.Skip("zstd is not installed")
	}
	root := t.TempDir()
	samples := make([]string, 0, 8)
	for i := 0; i < 8; i++ {
		name := filepath.Join(root, "sample-"+string(rune('0'+i)))
		sample := bytes.Repeat([]byte("user_id=42 action=login status=ok region=ru\n"), 20+i)
		if err := os.WriteFile(name, sample, 0600); err != nil {
			t.Fatal(err)
		}
		samples = append(samples, name)
	}
	dictPath := filepath.Join(root, "dict")
	args := append([]string{"--train", "-q", "-o", dictPath}, samples...)
	if output, err := exec.Command("zstd", args...).CombinedOutput(); err != nil {
		t.Skipf("zstd dictionary training unavailable: %v: %s", err, output)
	}
	dictionary, err := os.ReadFile(dictPath)
	if err != nil {
		t.Fatal(err)
	}
	data := []byte("user_id=42 action=logout status=ok region=ru\n")
	cmd := exec.Command("zstd", "-q", "-D", dictPath, "-c")
	cmd.Stdin = bytes.NewReader(data)
	foreign, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	reader, err := NewReaderWithOptions(bytes.NewReader(foreign), Options{Dictionary: dictionary})
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(decoded, data) {
		t.Fatal("formatted dictionary mismatch")
	}
	var own bytes.Buffer
	if _, err := encodeStream(bytes.NewReader(data), &own, Options{Dictionary: dictionary}); err != nil {
		t.Fatal(err)
	}
	cmd = exec.Command("zstd", "-q", "-d", "-D", dictPath, "-c")
	cmd.Stdin = bytes.NewReader(own.Bytes())
	decoded, err = cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(decoded, data) {
		t.Fatal("own formatted dictionary output mismatch")
	}
}

func TestMalformedStreamsAndLimits(t *testing.T) {
	base := make([]byte, 0)
	var encoded bytes.Buffer
	if _, err := encodeStream(bytes.NewReader([]byte("payload")), &encoded, Options{}); err != nil {
		t.Fatal(err)
	}
	base = encoded.Bytes()
	mutations := []struct {
		name string
		fn   func([]byte)
	}{
		{"magic", func(data []byte) { data[0] ^= 1 }},
		{"reserved descriptor", func(data []byte) { data[4] |= 1 << 3 }},
		{"reserved block", func(data []byte) { data[6] |= 3 << 1 }},
		{"checksum", func(data []byte) { data[len(data)-1] ^= 1 }},
	}
	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			data := append([]byte(nil), base...)
			mutation.fn(data)
			reader := NewReader(bytes.NewReader(data))
			_, err := io.ReadAll(reader)
			requireError(t, err)
		})
	}
	for size := 0; size < len(base); size++ {
		reader := NewReader(bytes.NewReader(base[:size]))
		_, err := io.ReadAll(reader)
		requireError(t, err)
	}

	reader, err := NewReaderWithOptions(bytes.NewReader(base), Options{MaxOutputSize: 3})
	if err != nil {
		t.Fatal(err)
	}
	_, err = io.ReadAll(reader)
	if !errors.Is(err, ErrResourceLimit) {
		t.Fatalf("limit error = %v", err)
	}

	var skippable [10]byte
	binary.LittleEndian.PutUint32(skippable[0:4], skippableMagicBase)
	binary.LittleEndian.PutUint32(skippable[4:8], 2)
	skippable[8], skippable[9] = 1, 2
	reader, err = NewReaderWithOptions(bytes.NewReader(skippable[:]), Options{MaxSkippableSize: 1})
	if err != nil {
		t.Fatal(err)
	}
	_, err = io.ReadAll(reader)
	if !errors.Is(err, ErrResourceLimit) {
		t.Fatalf("skippable limit error = %v", err)
	}
}

func requireError(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestRandomInputsDoNotPanic(t *testing.T) {
	var state uint32 = 0x13579bdf
	for i := 0; i < 1000; i++ {
		length := int(state % 256)
		data := make([]byte, length)
		for j := range data {
			state ^= state << 13
			state ^= state >> 17
			state ^= state << 5
			data[j] = byte(state)
		}
		func() {
			defer func() {
				if recovered := recover(); recovered != nil {
					t.Fatalf("decoder panicked on input %d: %v", i, recovered)
				}
			}()
			_, _ = io.ReadAll(NewReader(bytes.NewReader(data)))
		}()
	}
}
