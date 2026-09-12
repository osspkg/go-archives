# go-archives

[![Coverage Status](https://coveralls.io/repos/github/osspkg/go-archives/badge.svg?branch=master)](https://coveralls.io/github/osspkg/go-archives?branch=master)
[![Release](https://img.shields.io/github/release/osspkg/go-archives.svg?style=flat-square)](https://github.com/osspkg/go-archives/releases/latest)
[![Go Report Card](https://goreportcard.com/badge/github.com/osspkg/go-archives)](https://goreportcard.com/report/github.com/osspkg/go-archives)
[![CI](https://github.com/osspkg/go-archives/actions/workflows/ci.yml/badge.svg)](https://github.com/osspkg/go-archives/actions/workflows/ci.yml)

## Install

```sh
go get -u go.osspkg.com/archives
```

## Archives

* ar (Unix) - https://en.wikipedia.org/wiki/Ar_(Unix)
* xz (single-stream XZ) - https://tukaani.org/xz/format.html

The `xz` package provides a pure-Go XZ implementation without external
processes or runtime dependencies. XZ is a single-stream format, so an
archive has one logical member whose name is derived from the archive name:
`data.xz` exposes the member `data`. `OpenWithOptions` can cap the dictionary
and decompressed output size when processing untrusted archives.

## License

BSD-3-Clause License. See the LICENSE file for details.
