/*
 *  Copyright (c) 2021-2023 Mikhail Knyazhev <markus621@yandex.ru>. All rights reserved.
 *  Use of this source code is governed by a BSD 3-Clause license that can be found in the LICENSE file.
 */

package xz_test

import (
	"bytes"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"

	"go.osspkg.com/archives/xz"
)

func TestRoundTrip(t *testing.T) {
	root := t.TempDir()
	archivePath := filepath.Join(root, "data.xz")
	data := bytes.Repeat([]byte("hello xz "), 10000)

	a, err := xz.Open(archivePath, 0600)
	mustNoError(t, err)
	mustNoError(t, a.Write("data", data, 0600))
	mustNoError(t, a.Close())

	a, err = xz.Open(archivePath, 0600)
	mustNoError(t, err)
	defer a.Close()
	mustEqual(t, []xz.Header{{FileName: "data", Size: int64(len(data))}}, a.List())
	var got bytes.Buffer
	mustNoError(t, a.Read("data", &got))
	mustEqual(t, data, got.Bytes())
}

func TestImportExport(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "data")
	archivePath := filepath.Join(root, "data.xz")
	mustNoError(t, os.WriteFile(source, bytes.Repeat([]byte{'x'}, 200000), 0600))

	a, err := xz.Open(archivePath, 0600)
	mustNoError(t, err)
	mustNoError(t, a.Import(source, 0600))
	mustNoError(t, a.Close())

	a, err = xz.Open(archivePath, 0600)
	mustNoError(t, err)
	defer a.Close()
	outDir := filepath.Join(root, "out")
	mustNoError(t, a.Export("data", outDir))
	got, err := os.ReadFile(filepath.Join(outDir, "data"))
	mustNoError(t, err)
	mustEqual(t, bytes.Repeat([]byte{'x'}, 200000), got)
}

func TestOutputWorksWithXZUtils(t *testing.T) {
	if _, err := exec.LookPath("xz"); err != nil {
		t.Skip("xz is not installed")
	}
	root := t.TempDir()
	archivePath := filepath.Join(root, "data.xz")
	data := bytes.Repeat([]byte("compressible "), 1000)
	a, err := xz.Open(archivePath, 0600)
	mustNoError(t, err)
	mustNoError(t, a.Write("data", data, 0600))
	mustNoError(t, a.Close())

	decoded, err := exec.Command("xz", "-dc", archivePath).Output()
	mustNoError(t, err)
	mustEqual(t, data, decoded)

	externalPath := filepath.Join(root, "external.xz")
	cmd := exec.Command("xz", "-c")
	cmd.Stdin = bytes.NewReader(data)
	external, err := cmd.Output()
	mustNoError(t, err)
	mustNoError(t, os.WriteFile(externalPath, external, 0600))
	b, err := xz.Open(externalPath, 0600)
	mustNoError(t, err)
	defer b.Close()
	var got bytes.Buffer
	mustNoError(t, b.Read("external", &got))
	mustEqual(t, data, got.Bytes())
}

func TestExternalEmptyStream(t *testing.T) {
	if _, err := exec.LookPath("xz"); err != nil {
		t.Skip("xz is not installed")
	}
	encoded, err := exec.Command("xz", "-c").Output()
	mustNoError(t, err)
	path := filepath.Join(t.TempDir(), "empty.xz")
	mustNoError(t, os.WriteFile(path, encoded, 0600))
	a, err := xz.Open(path, 0600)
	mustNoError(t, err)
	defer a.Close()
	mustEqual(t, []xz.Header{{FileName: "empty", Size: 0}}, a.List())
	var got bytes.Buffer
	mustNoError(t, a.Read("empty", &got))
	mustEmpty(t, got.Bytes())
}

func TestShortWriter(t *testing.T) {
	root := t.TempDir()
	a, err := xz.Open(filepath.Join(root, "data.xz"), 0600)
	mustNoError(t, err)
	defer a.Close()
	mustNoError(t, a.Write("data", []byte("data"), 0600))
	mustErrorIs(t, a.Read("data", shortWriter{}), io.ErrShortWrite)
}

type shortWriter struct{}

func (shortWriter) Write([]byte) (int, error) { return 0, nil }

func TestRoundTripDataSets(t *testing.T) {
	random := make([]byte, 1<<20)
	var state uint32 = 0x12345678
	for i := range random {
		state ^= state << 13
		state ^= state >> 17
		state ^= state << 5
		random[i] = byte(state)
	}
	cases := [][]byte{
		{},
		[]byte("small xz payload"),
		bytes.Repeat([]byte("repeat me "), 10000),
		random,
		append(append([]byte(nil), random[:64<<10]...), bytes.Repeat([]byte{'x'}, 64<<10)...),
		bytes.Repeat([]byte("large repeated payload\n"), 1<<16),
	}
	for i, want := range cases {
		t.Run(string(rune('a'+i)), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "data.xz")
			a, err := xz.Open(path, 0600)
			mustNoError(t, err)
			mustNoError(t, a.Write("data", want, 0600))
			mustNoError(t, a.Close())

			a, err = xz.Open(path, 0600)
			mustNoError(t, err)
			defer a.Close()
			mustEqual(t, []xz.Header{{FileName: "data", Size: int64(len(want))}}, a.List())
			var got bytes.Buffer
			mustNoError(t, a.Read("data", &got))
			mustEqual(t, len(want), got.Len())
			if len(want) > 0 {
				mustEqual(t, want, got.Bytes())
			}
		})
	}
}

func TestLimitsAndClosedState(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "data.xz")
	a, err := xz.Open(path, 0600)
	mustNoError(t, err)
	mustNoError(t, a.Write("data", []byte("four"), 0600))
	mustNoError(t, a.Close())

	limited, err := xz.OpenWithOptions(path, 0600, xz.Options{MaxOutputSize: 3})
	mustError(t, err)
	mustErrorIs(t, err, xz.ErrResourceLimit)
	mustNil(t, limited)

	a, err = xz.Open(path, 0600)
	mustNoError(t, err)
	mustNoError(t, a.Close())
	mustNoError(t, a.Close())
	mustNil(t, a.List())
	mustErrorIs(t, a.Read("data", io.Discard), xz.ErrArchiveClosed)
	mustErrorIs(t, a.Write("data", []byte("x"), 0600), xz.ErrArchiveClosed)
	mustErrorIs(t, a.Export("data", root), xz.ErrArchiveClosed)
	mustErrorIs(t, a.Import(filepath.Join(root, "data"), 0600), xz.ErrArchiveClosed)
}

func TestNamesAndSelfImport(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "nested", "data.xz")
	mustNoError(t, os.MkdirAll(filepath.Dir(path), 0700))
	a, err := xz.Open(path, 0600)
	mustNoError(t, err)
	defer a.Close()
	mustEqual(t, []xz.Header{{FileName: "data", Size: 0}}, a.List())
	for _, name := range []string{"../data", "data/child", `/absolute`, `C:\\absolute`} {
		mustErrorIs(t, a.Read(name, io.Discard), xz.ErrInvalidFileName)
		mustErrorIs(t, a.Write(name, nil, 0600), xz.ErrInvalidFileName)
		mustErrorIs(t, a.Export(name, root), xz.ErrInvalidFileName)
	}
	mustError(t, a.Import(filepath.Join(root, "other"), 0600))
	mustError(t, a.Import(path, 0600))
}

func TestExportThroughSymlinkAndReplacement(t *testing.T) {
	root := t.TempDir()
	archivePath := filepath.Join(root, "data.xz")
	a, err := xz.Open(archivePath, 0600)
	mustNoError(t, err)
	mustNoError(t, a.Write("data", bytes.Repeat([]byte("old\n"), 20000), 0600))

	out := filepath.Join(root, "real-out")
	mustNoError(t, os.Mkdir(out, 0700))
	link := filepath.Join(root, "linked-out")
	if err := os.Symlink(out, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	mustNoError(t, a.Export("data", link))
	mustEqual(t, bytes.Repeat([]byte("old\n"), 20000), readFile(t, filepath.Join(out, "data")))

	want := []byte("new")
	mustNoError(t, a.Write("data", want, 0600))
	mustNoError(t, a.Export("data", link))
	mustEqual(t, want, readFile(t, filepath.Join(out, "data")))
	mustNoError(t, a.Close())

	if _, err := exec.LookPath("xz"); err == nil {
		decoded, err := exec.Command("xz", "-dc", archivePath).Output()
		mustNoError(t, err)
		mustEqual(t, want, decoded)
	}
}

func TestWriteAppliesArchivePermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.xz")
	a, err := xz.Open(path, 0600)
	mustNoError(t, err)
	mustNoError(t, a.Write("data", []byte("read-only archive"), 0440))
	stat, err := os.Stat(path)
	mustNoError(t, err)
	mustEqual(t, os.FileMode(0440), stat.Mode().Perm())
	mustNoError(t, a.Close())

	a, err = xz.Open(path, 0600)
	mustNoError(t, err)
	defer a.Close()
	var got bytes.Buffer
	mustNoError(t, a.Read("data", &got))
	mustEqual(t, []byte("read-only archive"), got.Bytes())
}

func TestConcurrentReads(t *testing.T) {
	want := bytes.Repeat([]byte("concurrent read "), 10000)
	a, err := xz.Open(filepath.Join(t.TempDir(), "data.xz"), 0600)
	mustNoError(t, err)
	mustNoError(t, a.Write("data", want, 0600))
	defer a.Close()

	const workers = 8
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var got bytes.Buffer
			if err := a.Read("data", &got); err != nil {
				errs <- err
				return
			}
			if !bytes.Equal(got.Bytes(), want) {
				errs <- errors.New("concurrent read returned different data")
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		mustNoError(t, err)
	}
}

func TestMalformedArchives(t *testing.T) {
	base := makeArchive(t, nil)
	mutateBlockHeader := func(data []byte, mutate func([]byte)) []byte {
		copyData := append([]byte(nil), data...)
		mutate(copyData)
		binary.LittleEndian.PutUint32(copyData[20:24], crc32.ChecksumIEEE(copyData[12:20]))
		return copyData
	}
	mutations := []struct {
		name string
		data func() []byte
	}{
		{"magic", func() []byte {
			data := append([]byte(nil), base...)
			data[0] ^= 1
			return data
		}},
		{"stream header crc", func() []byte {
			data := append([]byte(nil), base...)
			data[8] ^= 1
			return data
		}},
		{"block header crc", func() []byte {
			data := append([]byte(nil), base...)
			data[20] ^= 1
			return data
		}},
		{"block check", func() []byte {
			data := append([]byte(nil), base...)
			data[28] ^= 1
			return data
		}},
		{"index crc", func() []byte {
			data := append([]byte(nil), base...)
			data[len(data)-16] ^= 1
			return data
		}},
		{"footer magic", func() []byte {
			data := append([]byte(nil), base...)
			data[len(data)-1] ^= 1
			return data
		}},
		{"unsupported filter", func() []byte {
			return mutateBlockHeader(base, func(data []byte) { data[14] = 0x22 })
		}},
		{"dictionary limit", func() []byte {
			return mutateBlockHeader(base, func(data []byte) { data[16] = 40 })
		}},
		{"invalid dictionary property", func() []byte {
			return mutateBlockHeader(base, func(data []byte) { data[16] = 41 })
		}},
		{"index count", func() []byte {
			data := append([]byte(nil), base...)
			indexStart := len(data) - 20
			data[indexStart+1] = 0
			binary.LittleEndian.PutUint32(data[len(data)-16:len(data)-12], crc32.ChecksumIEEE(data[indexStart:len(data)-16]))
			return data
		}},
	}
	for _, tc := range mutations {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "data.xz")
			mustNoError(t, os.WriteFile(path, tc.data(), 0600))
			a, err := xz.Open(path, 0600)
			mustError(t, err)
			if a != nil {
				t.Errorf("Open returned an archive with error")
				_ = a.Close()
			}
		})
	}

	path := filepath.Join(t.TempDir(), "data.xz")
	mustNoError(t, os.WriteFile(path, mutateBlockHeader(base, func(data []byte) { data[16] = 40 }), 0600))
	_, err := xz.OpenWithOptions(path, 0600, xz.Options{MaxDictionarySize: 64 << 20})
	mustErrorIs(t, err, xz.ErrResourceLimit)
	for size := 1; size < len(base); size++ {
		truncated := filepath.Join(t.TempDir(), "data.xz")
		mustNoError(t, os.WriteFile(truncated, base[:size], 0600))
		_, err := xz.Open(truncated, 0600)
		if err == nil {
			t.Fatalf("expected error for truncated size %d", size)
		}
	}
}

func TestOpenDoesNotLeakFileDescriptorsOnErrors(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("/proc/self/fd is Linux-specific")
	}
	base := makeArchive(t, nil)
	base[20] ^= 1
	path := filepath.Join(t.TempDir(), "bad.xz")
	mustNoError(t, os.WriteFile(path, base, 0600))
	before := countOpenFDs(t)
	for i := 0; i < 100; i++ {
		_, err := xz.Open(path, 0600)
		mustError(t, err)
	}
	after := countOpenFDs(t)
	mustLessOrEqual(t, after, before+1)
}

func TestExternalCheckTypes(t *testing.T) {
	if _, err := exec.LookPath("xz"); err != nil {
		t.Skip("xz is not installed")
	}
	data := bytes.Repeat([]byte("check type "), 1000)
	for _, check := range []string{"none", "crc32", "sha256"} {
		t.Run(check, func(t *testing.T) {
			cmd := exec.Command("xz", "-c", "--check="+check)
			cmd.Stdin = bytes.NewReader(data)
			encoded, err := cmd.Output()
			mustNoError(t, err)
			path := filepath.Join(t.TempDir(), "data.xz")
			mustNoError(t, os.WriteFile(path, encoded, 0600))
			a, err := xz.Open(path, 0600)
			mustNoError(t, err)
			defer a.Close()
			var got bytes.Buffer
			mustNoError(t, a.Read("data", &got))
			mustEqual(t, data, got.Bytes())
		})
	}
}

func TestExternalMultipleBlocks(t *testing.T) {
	if _, err := exec.LookPath("xz"); err != nil {
		t.Skip("xz is not installed")
	}
	data := bytes.Repeat([]byte("multiple blocks "), 30000)
	cmd := exec.Command("xz", "-c", "--block-size=64KiB")
	cmd.Stdin = bytes.NewReader(data)
	encoded, err := cmd.Output()
	mustNoError(t, err)
	path := filepath.Join(t.TempDir(), "data.xz")
	mustNoError(t, os.WriteFile(path, encoded, 0600))
	a, err := xz.Open(path, 0600)
	mustNoError(t, err)
	defer a.Close()
	var got bytes.Buffer
	mustNoError(t, a.Read("data", &got))
	mustEqual(t, data, got.Bytes())
}

func makeArchive(t *testing.T, data []byte) []byte {
	t.Helper()
	path := filepath.Join(t.TempDir(), "data.xz")
	a, err := xz.Open(path, 0600)
	mustNoError(t, err)
	mustNoError(t, a.Write("data", data, 0600))
	mustNoError(t, a.Close())
	encoded, err := os.ReadFile(path)
	mustNoError(t, err)
	return encoded
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	mustNoError(t, err)
	return data
}

func countOpenFDs(t *testing.T) int {
	t.Helper()
	entries, err := os.ReadDir("/proc/self/fd")
	mustNoError(t, err)
	return len(entries)
}
