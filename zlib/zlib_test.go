package zlib

import (
	"bytes"
	stdzlib "compress/zlib"
	"errors"
	"io"
	"os"
	"testing"

	"github.com/abgoyal/aflate/flate"
)

var levels = []int{flate.HuffmanOnly, flate.DefaultCompression, 0, 1, 2, 3, 4, 5, 6, 7, 8, 9}

func inputs(t *testing.T) map[string][]byte {
	t.Helper()
	in := map[string][]byte{
		"empty": nil,
		"tiny":  []byte("q 1 0 0 1 50 700 cm BT /F1 12 Tf (x) Tj ET Q\n"),
	}
	// Mark Twain is ~380 KiB, so it spans several blocks.
	for _, fn := range []string{"../testdata/e.txt", "../testdata/Mark.Twain-Tom.Sawyer.txt"} {
		b, err := os.ReadFile(fn)
		if err != nil {
			t.Fatal(err)
		}
		in[fn] = b
	}
	return in
}

// stdlibInflate decodes with the standard library, not this package: a
// matching encoder/decoder pair can agree on a wrong bit layout.
func stdlibInflate(t *testing.T, compressed []byte) []byte {
	t.Helper()
	r, err := stdzlib.NewReader(bytes.NewReader(compressed))
	if err != nil {
		t.Fatalf("stdlib zlib reader: %v", err)
	}
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("stdlib zlib read: %v", err)
	}
	if err := r.Close(); err != nil {
		t.Fatalf("stdlib zlib close: %v", err)
	}
	return got
}

// TestWriterRoundTripStdlib compresses every input through one Writer per
// level, Reset between streams as a pooled caller does, and inflates each
// stream with the standard library.
func TestWriterRoundTripStdlib(t *testing.T) {
	in := inputs(t)
	for _, level := range levels {
		var buf bytes.Buffer
		z, err := NewWriter(&buf, level)
		if err != nil {
			t.Fatalf("level %d: %v", level, err)
		}
		for pass := 0; pass < 2; pass++ {
			for name, want := range in {
				buf.Reset()
				z.Reset(&buf)
				if _, err := z.Write(want); err != nil {
					t.Fatalf("level %d %s: write: %v", level, name, err)
				}
				if err := z.Close(); err != nil {
					t.Fatalf("level %d %s: close: %v", level, name, err)
				}
				if got := stdlibInflate(t, buf.Bytes()); !bytes.Equal(got, want) {
					t.Fatalf("level %d %s: round trip mismatch: got %d bytes, want %d",
						level, name, len(got), len(want))
				}
			}
		}
	}
}

// TestWriterManyWrites covers a stream built from many small writes, the way
// a caller accumulating rows uses it; the checksum must cover all of them.
func TestWriterManyWrites(t *testing.T) {
	want := inputs(t)["../testdata/e.txt"]
	var buf bytes.Buffer
	z, err := NewWriter(&buf, 5)
	if err != nil {
		t.Fatal(err)
	}
	for rest := want; len(rest) > 0; {
		n := min(len(rest), 97)
		if _, err := z.Write(rest[:n]); err != nil {
			t.Fatal(err)
		}
		rest = rest[n:]
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	if got := stdlibInflate(t, buf.Bytes()); !bytes.Equal(got, want) {
		t.Fatalf("round trip mismatch: got %d bytes, want %d", len(got), len(want))
	}
}

// TestWriterHeaderMatchesStdlib pins the RFC 1950 header, including its
// FLEVEL hint, to what the standard library writes at the same level.
func TestWriterHeaderMatchesStdlib(t *testing.T) {
	for _, level := range levels {
		var ours, std bytes.Buffer
		z, err := NewWriter(&ours, level)
		if err != nil {
			t.Fatal(err)
		}
		if err := z.Close(); err != nil {
			t.Fatal(err)
		}
		s, err := stdzlib.NewWriterLevel(&std, level)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(ours.Bytes()[:2], std.Bytes()[:2]) {
			t.Errorf("level %d: header % x, stdlib writes % x", level, ours.Bytes()[:2], std.Bytes()[:2])
		}
	}
}

func TestNewWriterRejectsLevel(t *testing.T) {
	// -64 must be refused here: flate would accept it, reading -32 and below
	// as a custom window size, and the header would then claim a 32 KiB one.
	for _, level := range []int{-3, -64, 10} {
		if _, err := NewWriter(io.Discard, level); err == nil {
			t.Errorf("level %d: want error", level)
		}
	}
}

// TestWriterStreamAllocatesNothing is the reason this package exists: a kept
// Writer compresses a stream with no allocation at all.
func TestWriterStreamAllocatesNothing(t *testing.T) {
	data := inputs(t)["../testdata/e.txt"][:6<<10]
	var buf bytes.Buffer
	buf.Grow(64 << 10)
	z, err := NewWriter(&buf, 5)
	if err != nil {
		t.Fatal(err)
	}
	allocs := testing.AllocsPerRun(100, func() {
		buf.Reset()
		z.Reset(&buf)
		if _, err := z.Write(data); err != nil {
			t.Fatal(err)
		}
		if err := z.Close(); err != nil {
			t.Fatal(err)
		}
	})
	if allocs != 0 {
		t.Errorf("Reset+Write+Close allocated %.1f times per stream, want 0", allocs)
	}
}

// TestWriterClosed pins what a closed Writer does: an error, not a panic
// and not a write that silently goes nowhere, until Reset starts a new stream.
func TestWriterClosed(t *testing.T) {
	var buf bytes.Buffer
	z, err := NewWriter(&buf, 5)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := z.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	if err := z.Close(); err == nil {
		t.Error("second Close: want error")
	}
	if _, err := z.Write(make([]byte, 100<<10)); err == nil {
		t.Error("Write after Close: want error")
	}
	buf.Reset()
	z.Reset(&buf)
	if _, err := z.Write([]byte("y")); err != nil {
		t.Fatalf("Write after Reset: %v", err)
	}
	if err := z.Close(); err != nil {
		t.Fatalf("Close after Reset: %v", err)
	}
	if got := stdlibInflate(t, buf.Bytes()); string(got) != "y" {
		t.Errorf("after Reset: got %q, want %q", got, "y")
	}
}

// failWriter fails every write after the first ok of them.
type failWriter struct{ ok int }

func (f *failWriter) Write(p []byte) (int, error) {
	if f.ok == 0 {
		return 0, errors.New("write failed")
	}
	f.ok--
	return len(p), nil
}

// TestWriterFailedWrites pins that a failing destination surfaces as an error
// and never as a panic or a silently short stream: a header that cannot be
// written, and a Close after a Write that failed.
func TestWriterFailedWrites(t *testing.T) {
	if _, err := NewWriter(&failWriter{}, 5); err == nil {
		t.Error("NewWriter: a header write that fails must be an error")
	}

	var ok bytes.Buffer
	z, err := NewWriter(&ok, 5)
	if err != nil {
		t.Fatal(err)
	}
	z.Reset(&failWriter{})
	if _, err := z.Write([]byte("x")); err == nil {
		t.Error("Write after a failed header write: want error")
	}

	// The header lands, then the compressed data does not: a large write
	// forces the encoder to flush a block before Close.
	z.Reset(&failWriter{ok: 1})
	_, werr := z.Write(bytes.Repeat([]byte("pdf content stream "), 1<<14))
	if err := z.Close(); werr == nil && err == nil {
		t.Error("a destination that fails after the header: Write and Close both reported success")
	}
	if err := z.Close(); err == nil {
		t.Error("second Close after a failure: want error")
	}
}

func stdlibDeflate(t *testing.T, data []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := stdzlib.NewWriter(&buf)
	if _, err := w.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// TestReaderReadsStdlib decodes the standard library's output, so the reader
// is checked against an encoder other than its own.
func TestReaderReadsStdlib(t *testing.T) {
	for name, want := range inputs(t) {
		r, err := NewReader(bytes.NewReader(stdlibDeflate(t, want)))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		got, err := io.ReadAll(r)
		if err != nil {
			t.Fatalf("%s: read: %v", name, err)
		}
		if err := r.Close(); err != nil {
			t.Fatalf("%s: close: %v", name, err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("%s: got %d bytes, want %d", name, len(got), len(want))
		}
	}
}

func TestReaderRejects(t *testing.T) {
	good := stdlibDeflate(t, []byte("hello, world"))
	corrupt := func(i int, b byte) []byte {
		c := bytes.Clone(good)
		c[i] = b
		return c
	}
	// header replaces CMF and FLG, recomputing FCHECK so that the field under
	// test is the only thing wrong with it.
	header := func(cmf, flg byte) []byte {
		c := bytes.Clone(good)
		c[0], c[1] = cmf, flg&^0x1f
		c[1] += uint8(31 - (uint16(c[0])<<8|uint16(c[1]))%31)
		return c
	}

	for _, tc := range []struct {
		name      string
		in        []byte
		atNew     error
		atReadEOF error
	}{
		{"bad FCHECK", corrupt(1, good[1]^1), ErrHeader, nil},
		{"not deflate", header(0x77, good[1]), ErrHeader, nil},
		{"window over 32 KiB", header(0x88, good[1]), ErrHeader, nil},
		{"preset dictionary", header(good[0], good[1]|0x20), ErrHeader, nil},
		{"short header", good[:1], io.ErrUnexpectedEOF, nil},
		{"bad checksum", corrupt(len(good)-1, good[len(good)-1]^1), nil, ErrChecksum},
		{"missing trailer", good[:len(good)-4], nil, io.ErrUnexpectedEOF},
	} {
		r, err := NewReader(bytes.NewReader(tc.in))
		if !errors.Is(err, tc.atNew) {
			t.Errorf("%s: NewReader error %v, want %v", tc.name, err, tc.atNew)
			continue
		}
		if err != nil {
			continue
		}
		_, err = io.ReadAll(r)
		if !errors.Is(err, tc.atReadEOF) {
			t.Errorf("%s: read error %v, want %v", tc.name, err, tc.atReadEOF)
		}
	}
}
