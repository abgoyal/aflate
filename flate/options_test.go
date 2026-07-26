package flate

import (
	"bytes"
	stdflate "compress/flate"
	"io"
	"math/rand"
	"testing"
)

// TestOptionsRoundtrip checks that every BlockSize still produces ordinary
// DEFLATE that the standard library decodes, across sizes that straddle the
// block boundary.
func TestOptionsRoundtrip(t *testing.T) {
	rng := rand.New(rand.NewSource(5))
	mk := func(n int) []byte {
		var b bytes.Buffer
		for b.Len() < n {
			b.WriteString("BT\n/F1 10 Tf\n1 0 0 1 50 ")
			b.WriteByte(byte('0' + rng.Intn(10)))
			b.WriteString(" Tm\n(row) Tj\nET\n")
		}
		return b.Bytes()
	}
	for _, bs := range []int{0, 4 << 10, 8 << 10, 16 << 10, 32 << 10, 1 << 20} {
		for _, lvl := range []int{-2, 0, 1, 5, 6, 9} { // every algorithm class
			for _, n := range []int{0, 1, 100, 5000, 20000, 70000} {
				src := mk(n)
				var buf bytes.Buffer
				w, err := NewWriterOptions(&buf, lvl, Options{BlockSize: bs})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := w.Write(src); err != nil {
					t.Fatalf("bs=%d L%d n=%d: %v", bs, lvl, n, err)
				}
				if err := w.Close(); err != nil {
					t.Fatalf("bs=%d L%d n=%d: close: %v", bs, lvl, n, err)
				}
				got, err := io.ReadAll(stdflate.NewReader(bytes.NewReader(buf.Bytes())))
				if err != nil {
					t.Fatalf("bs=%d L%d n=%d: stdlib inflate: %v", bs, lvl, n, err)
				}
				if !bytes.Equal(got, src) {
					t.Fatalf("bs=%d L%d n=%d: mismatch", bs, lvl, n)
				}
			}
		}
	}
}

// TestOptionsReuse checks a sized writer survives Reset across many streams.
func TestOptionsReuse(t *testing.T) {
	src := bytes.Repeat([]byte("BT /F1 10 Tf (x) Tj ET\n"), 400)
	var buf bytes.Buffer
	w, err := NewWriterOptions(&buf, 5, Options{BlockSize: 16 << 10})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 50; i++ {
		buf.Reset()
		w.Reset(&buf)
		w.Write(src)
		w.Close()
		got, err := io.ReadAll(stdflate.NewReader(bytes.NewReader(buf.Bytes())))
		if err != nil || !bytes.Equal(got, src) {
			t.Fatalf("iter %d: %v", i, err)
		}
	}
}
