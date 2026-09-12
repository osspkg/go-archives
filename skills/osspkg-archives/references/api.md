# API and usage

The module path is `go.osspkg.com/archives`. The public packages are:

| Package | Import path | Logical model |
| --- | --- | --- |
| `ar` | `go.osspkg.com/archives/ar` | Multiple named members in one archive |
| `xz` | `go.osspkg.com/archives/xz` | One member backed by one XZ stream |
| `zst` | `go.osspkg.com/archives/zst` | One member backed by concatenated Zstandard frames |

## Common lifecycle

All packages expose `Open`, `Close`, `List`, `Read`, `Write`, `Import`, and `Export`. `xz` and `zst` additionally expose `OpenWithOptions`.

```go
package main

import (
	"bytes"
	"fmt"
	"os"

	"go.osspkg.com/archives/zst"
)

func main() {
	a, err := zst.Open("data.zst", 0600)
	if err != nil {
		panic(err)
	}
	defer a.Close()

	if err := a.Write("data", []byte("hello"), 0600); err != nil {
		panic(err)
	}

	var out bytes.Buffer
	if err := a.Read("data", &out); err != nil {
		panic(err)
	}
	fmt.Fprintln(os.Stdout, out.String())
}
```

`Close` is safe to call repeatedly. After closing, operations that need the archive return `ErrArchiveClosed`; `xz.List` and `zst.List` return `nil`. `Read` streams to the supplied `io.Writer`, and short or invalid writers are reported as errors. Check and handle every returned error, including the deferred `Close` in code that must report close failures.

## Member names

`ar` accepts safe, explicit member names and can hold more than one member. `xz` and `zst` derive the only member name from the archive filename by removing the final `.xz` or `.zst` suffix. For example, `backup/data.zst` exposes `data`.

The single-member packages reject empty names, `.`, `..`, absolute paths, path separators (`/` and `\\`), and NUL bytes. Use the name returned by `List` for `Read`, `Write`, and `Export` rather than assuming a name from user input.

`Import` reads a regular source file and uses its basename as the member name. `Export` writes a member into the requested directory. `Write` replaces the compressed stream for `xz` and `zst`; `ar.Write` adds a new member and returns an error if that member already exists.

## Options

`xz.Options` controls `MaxDictionarySize`, `MaxOutputSize`, and `DictionarySize`.

`zst.Options` controls compression and frame limits:

```go
opts := zst.Options{
	CompressionLevel: 5,
	FrameSize:        8 << 20,
	WindowSize:       64 << 20,
	MaxWindowSize:    64 << 20,
	MaxOutputSize:    10_000_000_000,
	MaxSkippableSize: 16 << 20,
}

a, err := zst.OpenWithOptions("data.zst", 0600, opts)
```

Zero values select package defaults. The Zstandard defaults are level 3, 8 MiB frames, 128 KiB API block size, 64 MiB window and maximum window, 10 GB maximum output, and 16 MiB maximum skippable frame. `BlockSize` is retained by the API, but the external codec chooses the physical block size.

`zst.Dictionary` may be an official formatted Zstandard dictionary. A raw dictionary is accepted only with a non-zero `DictionaryID` for compatibility with the API, but the current external codec wrapper does not use it. A formatted dictionary's ID must match `DictionaryID` when one is supplied. `SkippableFrames` adds metadata frames before the data frames; readers skip valid skippable frames wherever they occur.

Use `errors.Is` with package sentinel errors such as `ErrFileNotFound`, `ErrInvalidFileName`, `ErrInvalidFormat`, `ErrResourceLimit`, and `ErrUnsupported` when the caller needs to distinguish failure classes.
