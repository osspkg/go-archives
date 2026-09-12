package zst_test

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"

	"go.osspkg.com/archives/zst"
)

func TestArchiveRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.zst")
	want := bytes.Repeat([]byte("archive data "), 10000)
	a, err := zst.Open(path, 0600)
	mustNoError(t, err)
	mustNoError(t, a.Write("data", want, 0600))
	mustNoError(t, a.Close())
	mustNoError(t, a.Close())

	a, err = zst.Open(path, 0600)
	mustNoError(t, err)
	defer a.Close()
	mustEqual(t, []zst.Header{{FileName: "data", Size: int64(len(want))}}, a.List())
	var got bytes.Buffer
	mustNoError(t, a.Read("data", &got))
	mustEqual(t, want, got.Bytes())
}

func TestArchiveImportExportAndReplacement(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "data.zst")
	source := filepath.Join(root, "data")
	want := bytes.Repeat([]byte("new\n"), 40000)
	mustNoError(t, os.WriteFile(source, want, 0600))
	a, err := zst.Open(path, 0600)
	mustNoError(t, err)
	mustNoError(t, a.Import(source, 0600))
	linkDir := filepath.Join(root, "link-out")
	outDir := filepath.Join(root, "out")
	mustNoError(t, os.Mkdir(outDir, 0700))
	if err := os.Symlink(outDir, linkDir); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	mustNoError(t, a.Export("data", linkDir))
	got, err := os.ReadFile(filepath.Join(outDir, "data"))
	mustNoError(t, err)
	mustEqual(t, want, got)
	mustNoError(t, a.Write("data", []byte("short"), 0600))
	mustNoError(t, a.Export("data", linkDir))
	got, err = os.ReadFile(filepath.Join(outDir, "data"))
	mustNoError(t, err)
	mustEqual(t, []byte("short"), got)
	mustError(t, a.Import(path, 0600))
	mustNoError(t, a.Close())
}

func TestArchiveNamesLimitsAndClosed(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "data.zst")
	a, err := zst.Open(path, 0600)
	mustNoError(t, err)
	for _, name := range []string{"../data", "data/child", "/absolute", `C:\\absolute`} {
		mustErrorIs(t, a.Read(name, io.Discard), zst.ErrInvalidFileName)
		mustErrorIs(t, a.Write(name, nil, 0600), zst.ErrInvalidFileName)
		mustErrorIs(t, a.Export(name, root), zst.ErrInvalidFileName)
	}
	mustNoError(t, a.Close())
	mustNoError(t, a.Close())
	mustNil(t, a.List())
	mustErrorIs(t, a.Read("data", io.Discard), zst.ErrArchiveClosed)
	mustErrorIs(t, a.Write("data", nil, 0600), zst.ErrArchiveClosed)
	mustErrorIs(t, a.Export("data", root), zst.ErrArchiveClosed)
	mustErrorIs(t, a.Import(filepath.Join(root, "data"), 0600), zst.ErrArchiveClosed)

	limitedPath := filepath.Join(root, "limited.zst")
	limited, err := zst.OpenWithOptions(limitedPath, 0600, zst.Options{MaxOutputSize: 3})
	mustNoError(t, err)
	mustErrorIs(t, limited.Write("limited", []byte("four"), 0600), zst.ErrResourceLimit)
	mustNoError(t, limited.Close())
}

func TestConcurrentReads(t *testing.T) {
	want := bytes.Repeat([]byte("concurrent "), 20000)
	a, err := zst.Open(filepath.Join(t.TempDir(), "data.zst"), 0600)
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
				errs <- errors.New("concurrent read mismatch")
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		mustNoError(t, err)
	}
}

func TestShortWriter(t *testing.T) {
	a, err := zst.Open(filepath.Join(t.TempDir(), "data.zst"), 0600)
	mustNoError(t, err)
	defer a.Close()
	mustNoError(t, a.Write("data", []byte("payload"), 0600))
	mustErrorIs(t, a.Read("data", shortWriter{}), io.ErrShortWrite)
}

type shortWriter struct{}

func (shortWriter) Write([]byte) (int, error) { return 0, nil }

func TestOpenErrorsDoNotLeakDescriptors(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("/proc/self/fd is Linux-specific")
	}
	path := filepath.Join(t.TempDir(), "bad.zst")
	mustNoError(t, os.WriteFile(path, []byte("bad"), 0600))
	before := countOpenFDs(t)
	for i := 0; i < 100; i++ {
		_, err := zst.Open(path, 0600)
		mustError(t, err)
	}
	after := countOpenFDs(t)
	mustLessOrEqual(t, after, before+1)
}

func countOpenFDs(t *testing.T) int {
	t.Helper()
	entries, err := os.ReadDir("/proc/self/fd")
	mustNoError(t, err)
	return len(entries)
}
