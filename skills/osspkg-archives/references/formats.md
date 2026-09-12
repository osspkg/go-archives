# Formats and compatibility

The packages intentionally expose a simple file API over different physical formats. Do not describe the logical member count as the physical frame or block count.

## `ar`

`ar` is a traditional archive with a global signature and multiple named records. Each member has its own name, mode, timestamp, and size in the archive header. Use it when callers need several named files in one archive.

## XZ

`xz` writes and reads a standard single XZ stream with stream header/footer, VLI fields, blocks, index, padding, and integrity checks. XZ itself does not store filenames, permissions, or timestamps. The package therefore derives one logical member from the archive basename and applies `perm` to the created `.xz` file. It does not implement TAR+XZ or a collection of named members.

Reference: [XZ file format](https://tukaani.org/xz/format.html).

## Zstandard

`zst` uses standard Zstandard frames. It can write multiple data frames when the input exceeds `FrameSize`, accepts concatenated standard frames, and accepts skippable frames in any position. The API still exposes one logical member whose uncompressed `Size` is the sum of all standard data frames; skippable-frame payloads are metadata and are not returned by `Read` or `Export`.

The package uses `github.com/klauspost/compress/zstd` rather than a copied local codec implementation. Keep the module dependency and wrapper boundary intact. The current dependency-backed writer always enables a frame checksum and uses encoder concurrency 1. `WindowSize`, `FrameSize`, and the output limits are package-level controls; `BlockSize` is an API compatibility field and is not a promise about the dependency's exact physical block partitioning.

Formatted official Zstandard dictionaries are checked for their dictionary ID and passed to the external codec. Raw dictionaries are not consumed by the current wrapper even though the API accepts them with a non-zero `DictionaryID`; document this limitation rather than claiming raw-dictionary round trips.

References:

- [Zstandard compression format](https://github.com/facebook/zstd/blob/dev/doc/zstd_compression_format.md)
- [RFC 8878](https://www.rfc-editor.org/rfc/rfc8878)
- [klauspost/compress zstd package](https://pkg.go.dev/github.com/klauspost/compress/zstd)

The repository remains on `go 1.17` and must not gain additional dependencies for these archive APIs. Compatibility tests may compare output with installed command-line tools, but production code remains self-contained Go code apart from the declared Zstandard module dependency.
