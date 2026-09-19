// Copyright 2009 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package zlib reads and writes zlib format compressed data (RFC 1950): a
// two-byte header, a DEFLATE stream, and an Adler-32 trailer. It is the
// framing PDF's FlateDecode filter expects.
//
// It is deliberately narrower than compress/zlib: one writer constructor that
// builds its encoder up front, no preset dictionaries (a PDF reader cannot be
// given one), no Flush, and a reader that takes a flate.Reader -- an io.Reader
// that is also an io.ByteReader -- so it never has to wrap its input in a
// buffer.
package zlib

import (
	"encoding/binary"
	"errors"
	"fmt"
	"hash"
	"hash/adler32"
	"io"

	"github.com/abgoyal/aflate/flate"
)

// errClosed is a Writer's sticky error between Close and the next Reset. The
// flate encoder drops its destination on Close, so without it a second Close
// or a Write would reach a nil writer.
var errClosed = errors.New("zlib: writer is closed")

// A Writer compresses what is written to it into one zlib stream on the
// underlying io.Writer. It is meant to be kept and Reset per stream: Reset
// allocates nothing, while NewWriter builds a whole flate encoder.
type Writer struct {
	w       io.Writer
	fw      *flate.Writer
	digest  hash.Hash32
	err     error
	header  [2]byte
	trailer [4]byte
}

// NewWriter returns a Writer compressing at level into a zlib stream on w.
// The level is flate.HuffmanOnly, flate.DefaultCompression, or 0 through 9.
func NewWriter(w io.Writer, level int) (*Writer, error) {
	if level < flate.HuffmanOnly || level > flate.BestCompression {
		return nil, fmt.Errorf("zlib: invalid compression level: %d", level)
	}
	fw, err := flate.NewWriter(w, level)
	if err != nil {
		return nil, err
	}
	z := &Writer{fw: fw, digest: adler32.New()}
	// RFC 1950 section 2.2. CMF: CINFO 7 (a 32 KiB window) and CM 8 (deflate).
	// FLG: the FLEVEL hint in the top two bits, no FDICT, and FCHECK making
	// the pair a multiple of 31.
	z.header[0] = 0x78
	switch level {
	case flate.HuffmanOnly, 0, 1:
		z.header[1] = 0 << 6
	case 2, 3, 4, 5:
		z.header[1] = 1 << 6
	case 6, flate.DefaultCompression:
		z.header[1] = 2 << 6
	default: // 7, 8, 9
		z.header[1] = 3 << 6
	}
	z.header[1] += uint8(31 - binary.BigEndian.Uint16(z.header[:])%31)
	z.Reset(w)
	if z.err != nil {
		return nil, z.err
	}
	return z, nil
}

// Reset starts a new stream on w, keeping the level. It writes
// the header to w; a failure to do so is returned by the next Write or Close.
func (z *Writer) Reset(w io.Writer) {
	z.w = w
	z.fw.Reset(w)
	z.digest.Reset()
	_, z.err = w.Write(z.header[:])
}

// Write compresses p into the stream. The compressed bytes are not all written
// to the underlying io.Writer until Close.
func (z *Writer) Write(p []byte) (int, error) {
	if z.err != nil {
		return 0, z.err
	}
	n, err := z.fw.Write(p)
	z.digest.Write(p[:n])
	z.err = err
	return n, err
}

// Close ends the stream: it flushes the final block and writes the checksum.
// It does not close the underlying io.Writer. Until the next Reset, Write and
// a further Close return an error.
func (z *Writer) Close() error {
	if z.err != nil {
		return z.err
	}
	if z.err = z.fw.Close(); z.err != nil {
		return z.err
	}
	// ZLIB (RFC 1950) is big-endian, unlike GZIP (RFC 1952).
	binary.BigEndian.PutUint32(z.trailer[:], z.digest.Sum32())
	if _, z.err = z.w.Write(z.trailer[:]); z.err != nil {
		return z.err
	}
	z.err = errClosed
	return nil
}
