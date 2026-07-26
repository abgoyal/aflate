package flate

// Cross-stream Huffman table + header caching.
//
// Context: a streaming PDF generator emits thousands of near-identical page
// content streams, each its own DEFLATE stream. Every one of them rebuilds a
// Huffman code from scratch and re-serialises a dynamic block header
// describing it, even though consecutive pages share a vocabulary (same
// operators, same font and colour names, similar digit distributions).
//
// A zlib *preset dictionary* would be the natural fix, but it is unusable in
// PDF: a reader has no way to be handed the dictionary, and the file fails to
// inflate. What is legally transferable between independent streams is not
// match history but the *entropy model* — and, crucially, the exact bits that
// encode it.
//
// A first attempt cached only the code table and skipped huffmanEncoder
// .generate. That measured ~0: generate is already cheap, and reuse forces the
// header to declare the cached table's full alphabet extent, which made the
// remaining header work *more* expensive. This version therefore caches the
// serialised header bits as well, so a reused table costs neither code
// construction (generate, generateCodegen, codegenEncoding.generate) nor
// header serialisation (writeDynamicHeader) — only a coverage check.
//
// The header is bit-identical across streams because it is always written at
// bit offset 0 of a fresh stream, and its only stream-dependent bit is BFINAL
// (bit 0), which is patched in on replay.

// warmHeaderCache enables the optimisation. It costs ~0.17% compression ratio
// for ~5% throughput on small streams; set to false to compile it out.
const warmHeaderCache = true

// warmTolShift bounds how much worse than a freshly built table the cached one
// may be before it is rejected: the allowance is 1>>warmTolShift of the
// estimated fresh size. Larger = stricter.
const warmTolShift = 8

// hdrCache holds a serialised dynamic block header together with the bit
// state left behind after writing it.
type hdrCache struct {
	valid bool

	buf    [bufferFlushSize + 8]byte // whole bytes emitted by the header
	nbytes uint8
	bits   uint64 // residual partial byte(s)
	nbits  uint8

	size           int // header size in bits, for lastHeader bookkeeping
	numLit, numOff int
}

// warmUsable reports whether the cached header can encode this block.
//
// Every symbol the block uses must carry a non-zero code length in the cached
// table, and the block must not use a symbol outside the alphabet the cached
// header declared. Note the cached header fixes numLiterals/numOffsets, so
// unlike a freshly built table we cannot shrink or grow the declared alphabet
// — we can only check that the block fits inside it.
func (w *huffmanBitWriter) warmUsable(numLiterals, numOffsets int) bool {
	h := &w.hdr
	if !warmHeaderCache || !h.valid || numLiterals > h.numLit || numOffsets > h.numOff {
		return false
	}
	lits := w.literalEncoding.codes
	for i, f := range w.literalFreq[:numLiterals] {
		if f != 0 && lits[i].len() == 0 {
			return false
		}
	}
	offs := w.offsetEncoding.codes
	for i, f := range w.offsetFreq[:numOffsets] {
		if f != 0 && offs[i].len() == 0 {
			return false
		}
	}
	return true
}

// atStreamStart reports that nothing has been emitted yet, which is the only
// position at which a cached header (captured from bit offset 0) is valid.
func (w *huffmanBitWriter) atStreamStart() bool {
	return w.nbytes == 0 && w.nbits == 0 && w.lastHeader == 0
}

// captureHeader records the bits just written by writeDynamicHeader so a later
// stream can replay them. writes counts calls to the underlying io.Writer; if
// it moved, the header spilled out of w.bytes and cannot be replayed.
func (w *huffmanBitWriter) captureHeader(size, numLiterals, numOffsets int, writesBefore uint32) {
	if w.err != nil || w.writes != writesBefore {
		return
	}
	h := &w.hdr
	copy(h.buf[:], w.bytes[:w.nbytes])
	h.nbytes = w.nbytes
	h.bits = w.bits
	h.nbits = w.nbits
	h.size = size
	h.numLit = numLiterals
	h.numOff = numOffsets
	h.valid = true
}

// replayHeader emits the cached header bytes verbatim and restores the trailing
// bit state, patching BFINAL to match this block.
func (w *huffmanBitWriter) replayHeader(isEof bool) {
	h := &w.hdr
	copy(w.bytes[:h.nbytes], h.buf[:h.nbytes])
	w.nbytes = h.nbytes
	w.bits = h.bits
	w.nbits = h.nbits

	// BFINAL is bit 0 of the stream. writeDynamicHeader wrote firstBits=4 for
	// a non-final block and 5 for a final one, i.e. it differs only in bit 0.
	if h.nbytes > 0 {
		if isEof {
			w.bytes[0] |= 1
		} else {
			w.bytes[0] &^= 1
		}
	} else if isEof {
		w.bits |= 1
	} else {
		w.bits &^= 1
	}
}
