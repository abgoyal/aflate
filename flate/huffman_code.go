// Copyright 2009 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package flate

import (
	"math"
	"math/bits"
)

const (
	maxBitsLimit = 16
	// number of valid literals
	literalCount = 286
)

// hcode is a huffman code with a bit code and bit length.
type hcode uint32

func (h hcode) len() uint8 {
	return uint8(h)
}

func (h hcode) code64() uint64 {
	return uint64(h >> 8)
}

func (h hcode) zero() bool {
	return h == 0
}

type huffmanEncoder struct {
	codes []hcode

	// Reusable scratch for code construction, sized for the largest alphabet
	// (literalCount) so that generate never allocates. See huffman_fast.go.
	nodes     [literalCount + 1]uint32
	counters  [literalCount + 1]uint16
	lens      [literalCount + 1]uint8
	lenCounts [maxBitsLimit + 1]int32
}

type literalNode struct {
	literal uint16
	freq    uint16
}

func (h *hcode) set(code uint16, length uint8) {
	*h = hcode(length) | (hcode(code) << 8)
}

func newhcode(code uint16, length uint8) hcode {
	return hcode(length) | (hcode(code) << 8)
}

func reverseBits(number uint16, bitLength byte) uint16 {
	return bits.Reverse16(number << ((16 - bitLength) & 15))
}

func maxNode() literalNode { return literalNode{math.MaxUint16, math.MaxUint16} }

func newHuffmanEncoder(size int) *huffmanEncoder {
	// Make capacity to next power of two.
	c := uint(bits.Len32(uint32(size - 1)))
	return &huffmanEncoder{codes: make([]hcode, size, 1<<c)}
}

// Generates a HuffmanCode corresponding to the fixed literal table
func generateFixedLiteralEncoding() *huffmanEncoder {
	h := newHuffmanEncoder(literalCount)
	codes := h.codes
	var ch uint16
	for ch = range uint16(literalCount) {
		var bits uint16
		var size uint8
		switch {
		case ch < 144:
			// size 8, 000110000  .. 10111111
			bits = ch + 48
			size = 8
		case ch < 256:
			// size 9, 110010000 .. 111111111
			bits = ch + 400 - 144
			size = 9
		case ch < 280:
			// size 7, 0000000 .. 0010111
			bits = ch - 256
			size = 7
		default:
			// size 8, 11000000 .. 11000111
			bits = ch + 192 - 280
			size = 8
		}
		codes[ch] = newhcode(reverseBits(bits, size), size)
	}
	return h
}

func generateFixedOffsetEncoding() *huffmanEncoder {
	h := newHuffmanEncoder(30)
	codes := h.codes
	for ch := range codes {
		codes[ch] = newhcode(reverseBits(uint16(ch), 5), 5)
	}
	return h
}

var fixedLiteralEncoding = generateFixedLiteralEncoding()
var fixedOffsetEncoding = generateFixedOffsetEncoding()

func (h *huffmanEncoder) bitLength(freq []uint16) int {
	var total int
	for i, f := range freq {
		if f != 0 {
			total += int(f) * int(h.codes[i].len())
		}
	}
	return total
}

func (h *huffmanEncoder) bitLengthRaw(b []byte) int {
	var total int
	for _, f := range b {
		total += int(h.codes[f].len())
	}
	return total
}

// canReuseBits returns the number of bits or math.MaxInt32 if the encoder cannot be reused.
func (h *huffmanEncoder) canReuseBits(freq []uint16) int {
	var total int
	for i, f := range freq {
		if f != 0 {
			code := h.codes[i]
			if code.zero() {
				return math.MaxInt32
			}
			total += int(f) * int(code.len())
		}
	}
	return total
}

// Return the number of literals assigned to each bit size in the Huffman encoding
//
// This method is only called when list.length >= 3
// The cases of 0, 1, and 2 literals are handled by special case code.
//
// list  An array of the literals with non-zero frequencies
//
//	and their associated frequencies. The array is in order of increasing
//	frequency, and has as its last element a special element with frequency
//	MaxInt32
//
// maxBits     The maximum number of bits that should be used to encode any literal.
//
//	Must be less than 16.
//
// return      An integer array in which array[i] indicates the number of literals
//
//	that should be encoded in i bits.
// atLeastOne clamps the result between 1 and 15.
func atLeastOne(v float32) float32 {
	if v < 1 {
		return 1
	}
	if v > 15 {
		return 15
	}
	return v
}

func histogram(b []byte, h []uint16) {
	if true && len(b) >= 8<<10 {
		// Split for bigger inputs
		histogramSplit(b, h)
	} else {
		h = h[:256]
		for _, t := range b {
			h[t]++
		}
	}
}

func histogramSplit(b []byte, h []uint16) {
	// Tested, and slightly faster than 2-way.
	// Writing to separate arrays and combining is also slightly slower.
	h = h[:256]
	for len(b)&3 != 0 {
		h[b[0]]++
		b = b[1:]
	}
	n := len(b) / 4
	x, y, z, w := b[:n], b[n:], b[n+n:], b[n+n+n:]
	y, z, w = y[:len(x)], z[:len(x)], w[:len(x)]
	for i, t := range x {
		v0 := &h[t]
		v1 := &h[y[i]]
		v2 := &h[z[i]]
		v3 := &h[w[i]]
		*v0++
		*v1++
		*v2++
		*v3++
	}
}
