# Syncing with klauspost/compress

Base: `117430d3b0e3c39c14d32fe7c90652149a78e609`

The directory layout mirrors upstream (`flate/`, `internal/le/`) and the package
name is unchanged, so a sync is a normal rebase. Only two things differ
structurally: `go.mod` declares `github.com/abgoyal/aflate`, and `flate/*.go`
import `github.com/abgoyal/aflate/internal/le` instead of
`github.com/klauspost/compress/internal/le`.

```sh
git remote add upstream https://github.com/klauspost/compress.git   # once
git fetch upstream
git checkout -b sync upstream/master -- flate internal/le testdata
sed -i 's|github.com/klauspost/compress/internal/le|github.com/abgoyal/aflate/internal/le|g' flate/*.go
git rebase   # reapply the two patches
```

`zlib/` is not synced: it is this fork's own package, written for a PDF
writer's needs rather than copied from upstream's `zlib`, so a sync leaves it
alone.

Files this fork owns (expect conflicts only here):

| file | change |
|---|---|
| `flate/huffman_fast.go` | new — libdeflate Huffman construction |
| `flate/huffman_code.go` | package-merge path deleted, scratch fields changed |
| `flate/huffman_bit_writer.go` | `writeTokens` rewritten; header-cache hooks |
| `flate/warmtable.go` | new — cross-stream header cache |
| `flate/huffman_sortByFreq.go` | deleted |
| `flate/huffman_sortByLiteral.go` | deleted |

After any sync:

```sh
go test ./...
go test -run TestFuzz -fuzz FuzzEncoding -fuzztime 60s ./flate/
```

and re-run a round-trip of real output through the **standard library**
inflater — a matching encoder/decoder pair can agree on a wrong bit layout.
