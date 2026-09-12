/*
 *  Copyright (c) 2021-2023 Mikhail Knyazhev <markus621@yandex.ru>. All rights reserved.
 *  Use of this source code is governed by a BSD 3-Clause license that can be found in the LICENSE file.
 */

package ar_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"go.osspkg.com/archives/ar"
)

const demoDeb = "ITxhcmNoPgpkZWJpYW4tYmluYXJ5ICAgMTY0NTMxODYwMSAgMCAgICAgMCAgICAgMTAwNjQ0ICA0ICAgICAgICAgYAoyLjAKY29" +
	"udHJvbC50YXIuZ3ogIDE2NDUzMTg2MDEgIDAgICAgIDAgICAgIDEwMDY0NCAgNzEzICAgICAgIGAKH4sIAAAAAAAA/+yYz27bNhjAfeZTcNl1lkj" +
	"9tbVh2IActsOAAMF2p6hPMTH9A0m3TU9tX6DHHvsK7p8AQYo4r0C/USHHbWOlrYG2kpGWvwsFEtRH8cOPH23HHfUOIYTEYbhuCSHddv1MA48EPi" +
	"UxpSNCYj/2Rjjsf2mj0VxpJkeEfO17uh93R3DcMgvVvFQ9xmj3IwqCT+afRuF2/tsmGOFBNvEHzz+bkDCfAqd0SqaT6SSjNMtjEnACE+6RDGPQ3E" +
	"1Tl9dVLk6cU1YWiPIQAhb4EzbxacrCOPNozOOcEvAhejdJnSoNZbZp3TR1FMh7ggPa90db3uO0idWyLnqMscv/kES3/I8j6/8QHDH+PzuBBKcpOq" +
	"7nkl8//gdSibpKMHGIQ9Gfks+EBq7nEhLMyiwK0D9MVJqJCmSC/1Ug8W9zBfIPeMDKpgCH1+Xv6O9KaVYUkI2PxUNIMEWH0ECVqQRvTodfMGdjDlK" +
	"LXHCmQaFj4Hod+j6k6EiKWgp9muC6aXtZgf6qS2jWS55p3ajEdW+EdNEhKC5Fc/0K88wszCuzNG/M0rw0S3OBzdJcmfPVY7Mwl+Z89RSbK7MwF+Zs" +
	"9cQsMMIOwua5WZrXq0dmaV6YS7M0Z51ZbcfWrH1n8ctZ+5/nooD+bgC7/Cde0PGfehG1/g+B+5Hyvu81WYbDcRsJolK6xxi7/PdCr+t/O2z9H4Cff" +
	"3JTUbkpUzOERI41KI3HOT5wP3uFP/gV6xlUCGO8qeRcF1jpumlvD9u9mVAsLaAd6I4wKOtqLKGoWdYZk6BAj3MmCshQLpA9lXrBcZta6X4PgJ31P/K" +
	"7/tM4tv4PwZb/N01mUrfGfuiCamPxvpds+Yas678se42xu/6T7u//wLf+D8Jdqf/73qfvlev63+8BsLP+01v//0d+aP0fgi3/970Yi8VisQzG2wAAA" +
	"P//+kw+rgAiAAAKZGF0YS50YXIuZ3ogICAgIDE2NDUzMTg2MDEgIDAgICAgIDAgICAgIDEwMDY0NCAgNDczICAgICAgIGAKH4sIAAAAAAAA/+yWQW" +
	"vbMBSAfdav0B+o/RTH8TD4sEEpZfTStOwQcpCtF1dMkYL0nC7/fiRmgYVAyRZ76+YvBwlJRNb7nv0UJ1HvAADkWXZoAeC0PfTFdALTVEAuRASQp/" +
	"kk4ln/jxZFbSDpI4Df/Z/Tw70T4gSp7jkHLvYvQEyz0f8QdP6rqs8U+AX/WQqj/yE4+q+dXekm3sm1ufYeb/kX+fToP8sgAjGZzfKIDxLE/9w/2m3" +
	"BFW4ZM64peKJwmwRSriVmcIum4CljL0SbgnEulfIFh/jwKwTAB2AKq7Y5MznZT7K1Vsrgq/S4X0Iv3hEZbZuCp+8nRv8y3fsfdoFwrXoqApd//8U" +
	"sHev/IPzsv2uvnQaX+0/FTIz+h+Cs/6qKA/qtrvEqe7zlP83ESf3PRCrG+j8Ei2erack+rgh9aZFenf8ak/QNEmOLeZcFS/Yc0JfeOWJ33rWbrvu" +
	"I+9hR6ezNSmrTevwxNMe6TCGwp90Gy6DXG4Ps9hvW88P6pA0+qbRNqor71vKbm+7yWZ65i7LP2pgHp7DceFdjCIeBuW6sNOX8/u7p9vGBscW9DSSN" +
	"WbIv0hKqT7tS4Uq2ho5n+dOBHhkZGfnL+B4AAP//x5J3eQAWAAAK"

func setUp(filename string, data string) error {
	bin, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		return fmt.Errorf("base64 decode: %w", err)
	}

	return os.WriteFile(filename, bin, fs.ModePerm)
}

func TestUnit_ArchiveRead(t *testing.T) {
	root := t.TempDir()
	demoPath := filepath.Join(root, "demo.deb")
	outputDir := filepath.Join(root, "123")
	mustNoError(t, setUp(demoPath, demoDeb))

	fd0, err := ar.Open(demoPath, os.ModePerm)
	mustNoError(t, err)
	mustNotNil(t, fd0)
	defer fd0.Close()

	buf := &bytes.Buffer{}

	mustNoError(t, fd0.Read("debian-binary", buf))
	mustEqual(t, "2.0\n", buf.String())

	mustNoError(t, fd0.Export("control.tar.gz", outputDir))

	fd1, err := os.Open(filepath.Join(outputDir, "control.tar.gz"))
	mustNoError(t, err)
	mustNotNil(t, fd1)
	defer fd1.Close()

	fd2, err := gzip.NewReader(fd1)
	mustNoError(t, err)
	mustNotNil(t, fd2)
	defer fd2.Close()

	fd3 := tar.NewReader(fd2)
	mustNotNil(t, fd3)

	list := func() []string {
		l := make([]string, 0)
		for {
			hdr, err := fd3.Next()
			if errors.Is(err, io.EOF) {
				return l
			}
			if err != nil {
				t.Fatal(err)
			}
			l = append(l, hdr.Name)
		}
	}()

	mustEqual(t, []string{"./", "./md5sums", "./control",
		"./conffiles", "./preinst", "./postinst", "./prerm", "./postrm"}, list)
}

func TestUnit_ArchiveCreate(t *testing.T) {
	root := t.TempDir()
	archivePath := filepath.Join(root, "demo1.ar")
	sourcePath := filepath.Join(root, "ddddd.txt")
	fd0, err := ar.Open(archivePath, os.ModePerm)
	mustNoError(t, err)
	mustNotNil(t, fd0)

	mustNoError(t, fd0.Write("file1", []byte("file1 text"), os.ModePerm))
	mustNoError(t, fd0.Write("file2", []byte("file2 text"), os.ModePerm))
	mustError(t, fd0.Write("file2", []byte("file2 text!"), os.ModePerm))
	mustNoError(t, os.WriteFile(sourcePath, []byte("ddddd file"), fs.ModePerm))
	mustNoError(t, fd0.Import(sourcePath, 0))
	fd0.Close()

	fd0, err = ar.Open(archivePath, os.ModePerm)
	mustNoError(t, err)
	mustNotNil(t, fd0)
	defer fd0.Close()

	buf := &bytes.Buffer{}

	mustNoError(t, fd0.Read("file1", buf))
	mustEqual(t, "file1 text", buf.String())

	buf.Reset()
	mustNoError(t, fd0.Read("file2", buf))
	mustEqual(t, "file2 text", buf.String())

	buf.Reset()
	mustNoError(t, fd0.Read("ddddd.txt", buf))
	mustEqual(t, "ddddd file", buf.String())

}

type blockingWriter struct {
	bytes.Buffer
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (w *blockingWriter) Write(p []byte) (int, error) {
	w.once.Do(func() {
		close(w.started)
		<-w.release
	})
	return w.Buffer.Write(p)
}

func TestUnit_ArchiveConcurrentRead(t *testing.T) {
	archivePath := filepath.Join(t.TempDir(), "concurrent.ar")
	fd, err := ar.Open(archivePath, 0600)
	mustNoError(t, err)
	defer fd.Close()
	mustNoError(t, fd.Write("a", bytes.Repeat([]byte{'a'}, 512), 0600))
	mustNoError(t, fd.Write("b", bytes.Repeat([]byte{'b'}, 512), 0600))

	first := &blockingWriter{started: make(chan struct{}), release: make(chan struct{})}
	firstDone := make(chan error)
	go func() {
		firstDone <- fd.Read("a", first)
	}()
	<-first.started

	second := &bytes.Buffer{}
	secondDone := make(chan error)
	go func() {
		secondDone <- fd.Read("b", second)
	}()
	mustNoError(t, <-secondDone)
	close(first.release)
	mustNoError(t, <-firstDone)

	mustEqual(t, bytes.Repeat([]byte{'a'}, 512), first.Bytes())
	mustEqual(t, bytes.Repeat([]byte{'b'}, 512), second.Bytes())
}

func TestUnit_ArchiveListAfterReopen(t *testing.T) {
	archivePath := filepath.Join(t.TempDir(), "list.ar")
	fd, err := ar.Open(archivePath, 0600)
	mustNoError(t, err)
	mustNoError(t, fd.Write("file", []byte("data"), 0600))
	mustNoError(t, fd.Close())

	fd, err = ar.Open(archivePath, 0600)
	mustNoError(t, err)
	defer fd.Close()
	mustLen(t, fd.List(), 1)
}

func TestUnit_ArchiveExportRejectsTraversalAndTruncates(t *testing.T) {
	root := t.TempDir()
	archivePath := filepath.Join(root, "archive.ar")
	fd, err := ar.Open(archivePath, 0600)
	mustNoError(t, err)
	defer fd.Close()
	mustNoError(t, fd.Write("../escaped", []byte("data"), 0600))
	mustErrorIs(t, fd.Export("../escaped", filepath.Join(root, "out")), ar.ErrInvalidFileName)
	_, err = os.Stat(filepath.Join(root, "escaped"))
	mustErrorIs(t, err, fs.ErrNotExist)

	mustNoError(t, fd.Write("file", []byte("new"), 0600))
	outputDir := filepath.Join(root, "output")
	mustNoError(t, os.MkdirAll(outputDir, 0700))
	mustNoError(t, os.WriteFile(filepath.Join(outputDir, "file"), []byte("old-tail"), 0600))
	mustNoError(t, fd.Export("file", outputDir))
	data, err := os.ReadFile(filepath.Join(outputDir, "file"))
	mustNoError(t, err)
	mustEqual(t, []byte("new"), data)
}

func TestUnit_ArchiveImportUsesBasename(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source.txt")
	mustNoError(t, os.WriteFile(source, []byte("source"), 0600))

	fd, err := ar.Open(filepath.Join(root, "import.ar"), 0600)
	mustNoError(t, err)
	defer fd.Close()
	mustNoError(t, fd.Import(source, 0600))

	var data bytes.Buffer
	mustNoError(t, fd.Read(filepath.Base(source), &data))
	mustEqual(t, []byte("source"), data.Bytes())
	mustErrorIs(t, fd.Read(source, io.Discard), ar.ErrFileNotFound)
}

func TestUnit_ArchiveRejectsInvalidSize(t *testing.T) {
	root := t.TempDir()
	header := fmt.Sprintf("%-16s%-12s%-6s%-6s%-8s%-10s%s", "x", "0", "0", "0", "100644", "-1", "`\n")
	archiveData := append([]byte("!<arch>\n"), []byte(header)...)
	archiveData = append(archiveData, 'x')
	archivePath := filepath.Join(root, "invalid.ar")
	mustNoError(t, os.WriteFile(archivePath, archiveData, 0600))

	_, err := ar.Open(archivePath, 0600)
	mustError(t, err)
}

func TestUnit_ArchiveImportRejectsSelf(t *testing.T) {
	root := t.TempDir()
	archivePath := filepath.Join(root, "self.ar")
	fd, err := ar.Open(archivePath, 0600)
	mustNoError(t, err)
	defer fd.Close()
	mustNoError(t, fd.Write("file", []byte("data"), 0600))
	before, err := os.Stat(archivePath)
	mustNoError(t, err)

	mustError(t, fd.Import(archivePath, 0600))
	after, err := os.Stat(archivePath)
	mustNoError(t, err)
	mustEqual(t, before.Size(), after.Size())
}
