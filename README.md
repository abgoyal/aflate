# aflate

A fork of [`klauspost/compress/flate`](https://github.com/klauspost/compress)
tuned for **many small streams** — the shape of a streaming PDF generator that
compresses one content stream per page through a pooled `Writer`.

Forked from klauspost/compress at `117430d3`.

```go
import "github.com/abgoyal/aflate/flate"

w, err := flate.NewWriter(dst, 6)   // drop-in: same API as klauspost/compress/flate
```

The package name is still `flate` and the directory layout mirrors upstream, so
rebasing on new upstream releases only has to reconcile the patches themselves,
not a rename.

## What is different

Two changes, both in the entropy-coding stage. Match finding, block splitting,
the level definitions and the public API are untouched.

### 1. libdeflate-style Huffman construction

Upstream builds each block's Huffman code with quicksort-by-frequency →
package-merge → a per-length quicksort-by-literal. This replaces that with
libdeflate's pipeline: a counting sort over packed `freq<<10|symbol` values, an
implicit tree over only the non-leaf nodes, a single depth pass, and canonical
codeword assignment in symbol order — which removes the sort-by-literal
entirely.

Table construction is a **per-block** cost, so it dominates when blocks are
small. On real PDF page content streams it was 27% of level-1 encode time.

### 2. Byte-granular bit flushing

Upstream flushes the bit accumulator in 48-bit units, leaving up to 15 bits
resident and only 33 bits of headroom — so it must check for a flush after
every field. A match writes four fields. Flushing at byte granularity (store 8
bytes, advance `nbits>>3`, keep `nbits&7`) leaves 57 bits of headroom, and a
match needs at most 48, so one flush per token replaces one per field.

### 3. Cross-stream header caching (optional)

A pooled `Writer` that is `Reset` between many similar streams rebuilds an
equivalent Huffman code and re-serialises an equivalent dynamic block header
every time. Since a dynamic header is always written at bit offset 0 of a fresh
stream, it is bit-identical for a given table except for `BFINAL`. This caches
the serialised bits and replays them when the cached table still fits, skipping
code construction and header serialisation both.

Costs ~0.17% compression ratio. Set `warmHeaderCache = false` in
`flate/warmtable.go` to compile it out; the rest of the fork is unaffected.

## Measured

854 real PDF page content streams (5.9 MB, median 6.3 KB), Go 1.26, i7-8700:

| level | upstream | aflate | speed | ratio |
|---|---|---|---|---|
| L1 | 164.2 MB/s | **274.4 MB/s** | **+67%** | 5.164 → 5.157 (−0.14%) |
| L5 | 119.3 MB/s | **171.2 MB/s** | **+44%** | 5.748 → 5.742 (−0.10%) |
| L6 | 108.8 MB/s | **150.8 MB/s** | **+39%** | 5.848 → 5.843 (−0.09%) |
| L9 | 41.4 MB/s | **48.6 MB/s** | **+17%** | 6.045 → 6.046 (+0.02%) |

Measured through a normal external import of this module, 0 allocs/op with a
pooled writer.

Gains grow as streams shrink, because what was optimised is per-block cost:
at 512-byte streams the first two changes alone are worth +52% at L6.

Without the optional header cache the ratio is *unchanged to fractionally
better* than upstream, and the gains are +54% / +37% / +30% / +16%.

## Compatibility

- Public API identical to `klauspost/compress/flate`.
- Output is ordinary DEFLATE (RFC 1951) — verified by round-tripping every test
  corpus through the **standard library's** inflater at every level, not just
  through this package's own.
- Upstream's full `flate` test suite passes, plus its fuzz corpus.
- Byte-for-byte output differs from upstream: equally valid Huffman codes are
  produced with different tie-breaking. Upstream's golden files were
  regenerated and came out a net 40 bytes *smaller*.

## Caveat worth knowing

The length-limited Huffman construction is **approximate** rather than optimal
(libdeflate clamps over-deep nodes to the deepest length in use; package-merge
is exact). Measured over 4000 adversarial synthetic frequency tables this costs
+0.024% in code size, and on real data it came out fractionally better. It only
matters when the 15-bit limit binds, which does not happen for DEFLATE's
alphabets in practice.

## Licence

BSD-3-Clause, inherited from klauspost/compress and the Go standard library.
The ported Huffman construction is MIT, from libdeflate. See `LICENSE`,
`LICENSE.libdeflate` and `NOTICE.md`.
