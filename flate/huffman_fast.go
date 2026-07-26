// Port of libdeflate's Huffman code construction
// (deflate_compress.c, MIT-licensed, Eric Biggers) into klauspost/compress's
// flate encoder.
//
// Upstream flate builds codes with:
//	quicksort by freq -> package-merge (bitCounts) -> per-length quicksort by literal
//
// Profiling on small PDF content streams put that whole path at ~27% of L1
// encode time (bitCounts alone at 17%), because the cost is per *block* and PDF
// blocks are small (a few KB), so it is amortized over very little data.
//
// libdeflate's construction is asymptotically and constantly cheaper:
//	counting sort by freq -> implicit tree over non-leaf nodes only ->
//	depth pass -> canonical assignment in symbol order
//
// Crucially it never sorts by literal: symbols land in canonical order for
// free because the counting sort is stable in symbol value and codewords are
// then handed out in symbol order via a next-codeword-per-length table.
//
// One semantic difference: package-merge produces *optimal* length-limited
// codes, whereas libdeflate clamps over-deep nodes to the longest length in
// use. That is approximate, but only when the 15-bit limit actually binds,
// which essentially never happens for DEFLATE's alphabets. Ratio is verified
// against upstream by TestFastHuffmanMatchesRatio.

package flate

import "math/bits"

const (
	numSymbolBits = 10
	symbolMask    = 1<<numSymbolBits - 1
	freqShift     = numSymbolBits
)

// sortSymbols performs libdeflate's counting sort: symbols ordered primarily by
// ascending frequency and secondarily by ascending symbol value, packed as
// freq<<10|sym. Zero-frequency symbols are dropped (and their codes cleared).
//
// Most symbol frequencies in a DEFLATE block are small, so a counter per
// possible frequency up to numSyms handles nearly everything in O(n); only the
// few symbols above that threshold need a comparison sort.
func (h *huffmanEncoder) sortSymbols(freq []uint16, out []uint32) int {
	numSyms := len(freq)
	counters := h.counters[:numSyms]
	for i := range counters {
		counters[i] = 0
	}
	maxCounter := uint16(numSyms - 1)

	for _, f := range freq {
		if f > maxCounter {
			f = maxCounter
		}
		counters[f]++
	}

	// Make cumulative, skipping index 0 (those are the discarded zero-freq
	// symbols). This also yields the count of used symbols.
	// Offsets fit in uint16: numSyms is at most literalCount (286).
	numUsed := uint16(0)
	for i := 1; i < numSyms; i++ {
		c := counters[i]
		counters[i] = numUsed
		numUsed += c
	}

	codes := h.codes[:numSyms]
	for sym, f := range freq {
		if f == 0 {
			codes[sym] = 0
			continue
		}
		c := f
		if c > maxCounter {
			c = maxCounter
		}
		out[counters[c]] = uint32(sym) | uint32(f)<<freqShift
		counters[c]++
	}

	// Only the saturated bucket can be out of order; sort just that span.
	lo, hi := counters[numSyms-2], counters[numSyms-1]
	if hi-lo > 1 {
		insertionSortU32(out[lo:hi])
	}
	return int(numUsed)
}

// insertionSortU32 sorts ascending. The saturated bucket holds symbols whose
// frequency reached numSyms, which for real blocks is a handful of entries, so
// insertion sort beats heapsort's setup cost here.
func insertionSortU32(a []uint32) {
	for i := 1; i < len(a); i++ {
		v := a[i]
		j := i - 1
		for j >= 0 && a[j] > v {
			a[j+1] = a[j]
			j--
		}
		a[j+1] = v
	}
}

// buildTree constructs only the non-leaf nodes of the Huffman tree, in place,
// exploiting the fact that both leaves and freshly created internal nodes are
// each already in ascending frequency order — so the next-smallest node is
// always one of two candidates and no heap is needed.
//
// On return A[i] holds its parent's index in the high bits, and A[symCount-2]
// is the root.
func buildTree(a []uint32) {
	symCount := len(a)
	lastIdx := symCount - 1
	i, b, e := 0, 0, 0 // next leaf, next unparented internal, next output slot
	for {
		var newFreq uint32
		switch {
		case i+1 <= lastIdx && (b == e || (a[i+1]&^symbolMask) <= (a[b]&^symbolMask)):
			// Two leaves.
			newFreq = (a[i] &^ symbolMask) + (a[i+1] &^ symbolMask)
			i += 2
		case b+2 <= e && (i > lastIdx || (a[b+1]&^symbolMask) < (a[i]&^symbolMask)):
			// Two internal nodes.
			newFreq = (a[b] &^ symbolMask) + (a[b+1] &^ symbolMask)
			a[b] = uint32(e)<<freqShift | (a[b] & symbolMask)
			a[b+1] = uint32(e)<<freqShift | (a[b+1] & symbolMask)
			b += 2
		default:
			// One leaf and one internal node.
			newFreq = (a[i] &^ symbolMask) + (a[b] &^ symbolMask)
			a[b] = uint32(e)<<freqShift | (a[b] & symbolMask)
			i++
			b++
		}
		a[e] = newFreq | (a[e] & symbolMask)
		e++
		if e >= lastIdx {
			return
		}
	}
}

// computeLengthCounts walks the non-leaf nodes parent-before-child (simply
// backwards through the array) turning parent indices into depths, and tallies
// how many codewords take each length. Depths beyond maxLen are clamped to the
// deepest length currently in use.
func computeLengthCounts(a []uint32, rootIdx int, lenCounts *[maxBitsLimit + 1]int32, maxLen int) {
	for i := 0; i <= maxLen; i++ {
		lenCounts[i] = 0
	}
	lenCounts[1] = 2

	a[rootIdx] &= symbolMask // root depth 0

	for node := rootIdx - 1; node >= 0; node-- {
		parent := a[node] >> freqShift
		depth := int(a[parent]>>freqShift) + 1
		a[node] = (a[node] & symbolMask) | uint32(depth)<<freqShift

		if depth >= maxLen {
			// Drop to the deepest length already in use. Note this must
			// decrement at least once (C's do/while): landing on maxLen
			// itself would push its two children to maxLen+1, past the
			// limit, and those codewords would never be assigned.
			depth = maxLen
			for {
				depth--
				if lenCounts[depth] != 0 {
					break
				}
			}
		}
		// This node is internal, not a leaf: it costs one codeword at this
		// depth and gains two at the next.
		lenCounts[depth]--
		lenCounts[depth+1] += 2
	}
}

// genCodewords assigns lengths to symbols (longest lengths to the lowest
// frequencies) then hands out canonical, bit-reversed codewords in symbol
// order. No sort by literal is needed: within a length, symbols are already
// visited in increasing value.
func (h *huffmanEncoder) genCodewords(a []uint32, lenCounts *[maxBitsLimit + 1]int32, maxLen, numSyms int) {
	lens := h.lens[:numSyms]

	i := 0
	for l := maxLen; l >= 1; l-- {
		for c := lenCounts[l]; c > 0; c-- {
			lens[a[i]&symbolMask] = uint8(l)
			i++
		}
	}

	var next [maxBitsLimit + 1]uint32
	for l := 2; l <= maxLen; l++ {
		next[l] = (next[l-1] + uint32(lenCounts[l-1])) << 1
	}

	codes := h.codes[:numSyms]
	for sym := 0; sym < numSyms; sym++ {
		l := lens[sym]
		if l == 0 {
			codes[sym] = 0 // zero-frequency symbol
			continue
		}
		cw := next[l]
		next[l]++
		codes[sym] = newhcode(uint16(bits.Reverse32(cw)>>(32-uint(l))), l)
	}
}

// generate updates this huffmanEncoder to be the minimum-cost length-limited
// code for the given symbol frequencies.
func (h *huffmanEncoder) generate(freq []uint16, maxBits int32) {
	numSyms := len(freq)
	a := h.nodes[:numSyms]

	numUsed := h.sortSymbols(freq, a)

	// Fewer than two used symbols is degenerate for tree building; DEFLATE
	// still needs every used symbol to carry a 1-bit code.
	if numUsed <= 2 {
		lens := h.lens[:numSyms]
		n := uint16(0)
		for sym := 0; sym < numSyms; sym++ {
			if freq[sym] != 0 {
				lens[sym] = 1
				h.codes[sym].set(n, 1)
				n++
			} else {
				lens[sym] = 0
			}
		}
		return
	}

	maxLen := int(maxBits)
	if maxLen > numUsed-1 {
		maxLen = numUsed - 1
	}
	if maxLen > maxBitsLimit-1 {
		maxLen = maxBitsLimit - 1
	}

	used := a[:numUsed]
	buildTree(used)
	computeLengthCounts(used, numUsed-2, &h.lenCounts, maxLen)

	// Zero the lengths of unused symbols so genCodewords skips them.
	for sym := 0; sym < numSyms; sym++ {
		if freq[sym] == 0 {
			h.lens[sym] = 0
		}
	}
	h.genCodewords(used, &h.lenCounts, maxLen, numSyms)
}
