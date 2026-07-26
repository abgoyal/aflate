package flate

import (
	"bytes"
	"compress/flate"
	"io"
	"math/rand"
	"testing"
)

// inflateStd decodes with the standard library, so a bug in this package's
// own inflater cannot mask an encoder bug.
func inflateStd(t *testing.T, b []byte) []byte {
	t.Helper()
	got, err := io.ReadAll(flate.NewReader(bytes.NewReader(b)))
	if err != nil {
		t.Fatalf("stdlib inflate: %v", err)
	}
	return got
}

// TestWarmHeaderReuse exercises the cross-stream header cache against the
// situations that can invalidate it: a following stream with a wider alphabet,
// a HuffmanOnly block (which swaps the literal encoder), inputs large enough
// to span several blocks, and empty streams.
func TestWarmHeaderReuse(t *testing.T) {
	if !warmHeaderCache {
		t.Skip("header cache disabled")
	}
	rng := rand.New(rand.NewSource(3))

	pdfish := func(n int, alphabet string) []byte {
		var b bytes.Buffer
		for b.Len() < n {
			b.WriteString("BT\n/F1 10 Tf\n0 0 0 rg\n1 0 0 1 ")
			b.WriteString(string(alphabet[rng.Intn(len(alphabet))]))
			b.WriteString(" 597.4 Tm\n(row ")
			b.WriteString(string(alphabet[rng.Intn(len(alphabet))]))
			b.WriteString(") Tj\nET\n")
		}
		return b.Bytes()
	}

	narrow := "0123456789"
	wide := "0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ!@#$%^&*()"

	for _, lvl := range []int{-2, 1, 5, 6, 9} {
		var buf bytes.Buffer
		w, err := NewWriter(&buf, lvl)
		if err != nil {
			t.Fatal(err)
		}
		// A mix designed to hit every cache path in sequence.
		inputs := [][]byte{
			pdfish(4000, narrow), // populates the cache
			pdfish(4000, narrow), // should hit the cache
			pdfish(4000, wide),   // wider alphabet: must not reuse blindly
			pdfish(4000, narrow), // back to narrow
			{},                   // empty stream
			pdfish(300000, wide), // multi-block: cache only valid at stream start
			pdfish(4000, narrow),
			bytes.Repeat([]byte{0}, 5000), // degenerate single-symbol
			pdfish(4000, narrow),
		}
		for i, in := range inputs {
			buf.Reset()
			w.Reset(&buf)
			if _, err := w.Write(in); err != nil {
				t.Fatalf("L%d input %d: write: %v", lvl, i, err)
			}
			if err := w.Close(); err != nil {
				t.Fatalf("L%d input %d: close: %v", lvl, i, err)
			}
			if got := inflateStd(t, buf.Bytes()); !bytes.Equal(got, in) {
				t.Fatalf("L%d input %d: roundtrip mismatch (%d in, %d out)",
					lvl, i, len(in), len(got))
			}
		}
	}
}

