---
name: osspkg-archives
description: Use the go-archives repository's ar, xz, and zst APIs correctly, including member naming, safe import/export, format compatibility, resource limits, and validation.
---

# go-archives

Use this skill for implementation, review, debugging, or documentation work involving this repository's archive packages.

## Working rules

- Inspect the current package source and tests before writing examples or changing an API. Keep documentation compatible with `go 1.17` and the signatures that are actually exported.
- Treat archive bytes, member names, archive paths, import paths, and export directories as untrusted input.
- `ar` stores multiple named members. `xz` and `zst` expose one logical member whose name comes from the archive basename (`data.xz` becomes `data`, and `data.zst` becomes `data`).
- Preserve the packages' atomic replacement, canonical-directory, temporary-file, cleanup, regular-file, and self-import protections. Do not change unrelated `ar/`, CI, or Makefile work unless the request includes it.
- The runtime library is pure Go and does not invoke system archive processes. The `zst` implementation uses the module dependency `github.com/klauspost/compress/zstd`; do not recreate a local codec copy.
- For code changes, run focused tests first and then the relevant repository checks: `gofmt`, `go test ./...`, `go test -race ./...`, `go vet ./...`, and `git diff --check`. Report checks that were skipped or blocked instead of implying they passed.

Read only the references relevant to the task:

- [API and examples](references/api.md) for package selection, lifecycle, member naming, options, and common operations.
- [Safety and validation](references/safety.md) for path handling, atomic writes, limits, cleanup, and regression checks.
- [Format and compatibility](references/formats.md) for the on-disk formats, external compatibility, and the current Zstandard dependency boundary.
