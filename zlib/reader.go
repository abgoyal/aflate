// Copyright 2009 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package zlib

import (
	"encoding/binary"
	"errors"
	"hash"
	"hash/adler32"
	"io"

	"github.com/abgoyal/aflate/flate"
)

var (
	// ErrChecksum is returned when a stream's Adler-32 trailer does not match
	// its decompressed data.
	ErrChecksum = errors.New("zlib: invalid checksum")
	// ErrHeader is returned when a stream's header is invalid, or asks for a
	// preset dictionary, which this package does not support.
	ErrHeader = errors.New("zlib: invalid header")
)

// A Reader decompresses one zlib stream.
type Reader struct {
	r       flate.Reader
	fr      io.ReadCloser
	digest  hash.Hash32
	err     error
	scratch [4]byte
}

// NewReader reads and checks the zlib header from r and returns a Reader
// over the stream. r must be an io.ByteReader (a *bytes.Reader or a
// *bufio.Reader is), so the decompressor reads exactly the stream and never
// has to buffer r itself.
//
// The checksum is verified only when the stream is read to io.EOF.
func NewReader(r flate.Reader) (*Reader, error) {
	z := &Reader{r: r}
	if _, err := io.ReadFull(r, z.scratch[:2]); err != nil {
		if err == io.EOF {
			err = io.ErrUnexpectedEOF
		}
		return nil, err
	}
	// RFC 1950 section 2.2: CM 8 (deflate), CINFO at most 7, FCHECK valid,
	// and FDICT clear.
	h := binary.BigEndian.Uint16(z.scratch[:2])
	if z.scratch[0]&0x0f != 8 || z.scratch[0]>>4 > 7 || h%31 != 0 || z.scratch[1]&0x20 != 0 {
		return nil, ErrHeader
	}
	z.fr = flate.NewReader(r)
	z.digest = adler32.New()
	return z, nil
}

// Read decompresses into p. At the end of the stream it checks the trailer and
// returns ErrChecksum instead of io.EOF if it does not match.
func (z *Reader) Read(p []byte) (int, error) {
	if z.err != nil {
		return 0, z.err
	}
	var n int
	n, z.err = z.fr.Read(p)
	z.digest.Write(p[:n])
	if z.err != io.EOF {
		return n, z.err
	}
	if _, err := io.ReadFull(z.r, z.scratch[:4]); err != nil {
		if err == io.EOF {
			err = io.ErrUnexpectedEOF
		}
		z.err = err
		return n, z.err
	}
	// ZLIB (RFC 1950) is big-endian, unlike GZIP (RFC 1952).
	if binary.BigEndian.Uint32(z.scratch[:4]) != z.digest.Sum32() {
		z.err = ErrChecksum
		return n, z.err
	}
	return n, io.EOF
}

// Close releases the decompressor. It does not close the underlying reader,
// and it reports a read error other than io.EOF if one occurred.
func (z *Reader) Close() error {
	if z.err != nil && z.err != io.EOF {
		return z.err
	}
	return z.fr.Close()
}
