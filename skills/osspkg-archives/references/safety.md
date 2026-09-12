# Safety and validation

Archive operations cross filesystem and compression trust boundaries. Keep the following behavior when modifying the packages.

## Paths and files

- Validate member names before joining them with a destination path. Reject traversal, absolute paths, both slash types, and NUL bytes; do not “clean” an unsafe name and then accept it.
- `Export` resolves the destination directory through `filepath.EvalSymlinks`, creates a temporary file in that canonical directory, streams the decoded data into it, closes it, and renames it into place. This protects against traversal, symlink substitution of the directory, and stale trailing bytes from an older destination.
- `Write` and `Import` encode into a temporary file in the archive directory, sync and close it, validate the result, and rename it atomically. An encoding or validation error must leave the previous archive intact.
- `Import` is limited to regular files and checks `os.SameFile` so an archive cannot be imported into itself. Always close the source on every return path.
- Temporary files, validation descriptors, source files, and archive descriptors need explicit cleanup paths. Preserve the existing deferred cleanup pattern when adding new errors.

## Resource limits and malformed input

Treat all sizes from archive bytes as attacker-controlled. Check integer conversions and additions before allocation, copying, seeking, or slicing. Enforce the configured dictionary, window, skippable-frame, and total-output limits before doing work that could exceed them. Truncated headers, blocks, frames, indexes, and checksums must return errors rather than panic or silently succeed.

The default decompressed-output limit is 10 GB in both stream packages. The default XZ dictionary limit is 64 MiB. The default Zstandard window limit is 64 MiB and the default skippable-frame limit is 16 MiB. Callers handling untrusted archives should lower these limits to the expected workload.

Writers must handle short writes. Use the package `writeAll` behavior or an equivalent loop that rejects negative, oversized, and zero-progress writes. A nil output writer is invalid. Do not add background goroutines to archive operations; keep ownership, cancellation, and cleanup synchronous and visible.

## Regression checks

For changes to archive code, cover at least the affected malformed-input and filesystem cases: bad magic/header/footer/block/checksum, truncation, invalid names, traversal, symlinked export directories, stale destination tails, atomic replacement after an induced failure, self-import, excessive output/dictionary/window/skippable sizes, short writers, closed archives, concurrent reads, and file-descriptor cleanup. Use external `xz` or `zstd` tools only as optional compatibility test oracles; the library itself must not spawn system processes.

Run the checks appropriate to the change:

```sh
gofmt -w <changed-go-files>
go test ./...
go test -race ./...
go vet ./...
git diff --check
```

If a dependency or vulnerability check cannot reach the network, report that limitation explicitly.
