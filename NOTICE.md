# Attribution

`aflate` is a derivative of the `flate` package from
[klauspost/compress](https://github.com/klauspost/compress), which is itself
derived from the Go standard library's `compress/flate`.

## Inherited licences

`LICENSE` is carried over unmodified from klauspost/compress. The parts that
apply to the code in this repository are:

- **Copyright (c) 2012 The Go Authors** — BSD-3-Clause. The original
  `compress/flate` implementation, which most of this package still descends
  from (`inflate.go`, `dict_decoder.go`, `token.go`, and the overall structure
  of `huffman_bit_writer.go`).
- **Copyright (c) 2019 Klaus Post** — BSD-3-Clause. The rewritten encoder:
  `level1.go` … `level6.go`, `fast_encoder.go`, `stateless.go`, and extensive
  reworking of everything else.
- **Copyright (c) 2011 The Snappy-Go Authors** — BSD-3-Clause. `fast_encoder.go`
  derives from Snappy's match finder.

`LICENSE` also contains licences for other packages of klauspost/compress
(zstd, s2 and their dependencies) that are **not** vendored here. It is kept
whole rather than trimmed, so nothing is accidentally dropped.

## Added code

`flate/huffman_fast.go` and the Huffman-related changes in `flate/huffman_code.go`
port the code-construction algorithm from
[libdeflate](https://github.com/ebiggers/libdeflate) by Eric Biggers, which is
**MIT licensed** — see `LICENSE.libdeflate`. Specifically: the counting sort
over packed `freq<<10|symbol` values, the implicit tree over non-leaf nodes,
the depth pass with approximate length limiting, and canonical codeword
assignment in symbol order.

The byte-granular bit flushing in `flate/huffman_bit_writer.go` (`writeTokens`)
follows the same project's `FLUSH_BITS` approach.

`flate/warmtable.go` (cross-stream header caching) is original work for this
fork and carries no third-party copyright.

## Compatibility

Both BSD-3-Clause and MIT are permissive and mutually compatible. Redistribution
requires only that these notices travel with the code. This repository does
that by keeping `LICENSE`, `LICENSE.libdeflate` and this file at its root, and
by preserving per-file copyright headers.
