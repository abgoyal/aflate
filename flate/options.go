package flate

import "io"

// Options tune a Writer's memory footprint. The zero value reproduces
// klauspost/compress behaviour exactly.
//
// A default Writer is sized for 64 KiB blocks and retains roughly 1 MiB:
// ~262 KiB of token buffer, ~320 KiB of match history, ~384 KiB of hash tables
// and ~85 KiB of everything else. Workloads that compress many small
// independent streams — a PDF generator emitting one content stream per page,
// say — never use most of that, and pay for it in resident memory across every
// pooled writer.
//
// Measured on real PDF page content streams (median 6.3 KiB): a block only ever
// produced 2026 tokens against a 65536-entry buffer, so the buffer was 32x
// larger than needed.
type Options struct {
	// BlockSize caps how much input is gathered before a block is emitted, in
	// bytes. It sizes both the staging window and the token buffer, which
	// together are more than half a Writer's footprint.
	//
	// Streams smaller than BlockSize are unaffected — they were already a
	// single block. Larger streams are split into more blocks, which costs a
	// little ratio because each block carries its own Huffman header.
	//
	// Zero means 64 KiB (the default). Values above that are clamped down to
	// it; very small values are still legal but will cost ratio on anything
	// bigger.
	BlockSize int
}

// NewWriterOptions is NewWriter with control over the Writer's memory
// footprint. See Options.
//
// The output is ordinary DEFLATE regardless of the options chosen: BlockSize
// only affects how the input is divided into blocks, which any decoder handles.
func NewWriterOptions(w io.Writer, level int, o Options) (*Writer, error) {
	var dw Writer
	if err := dw.d.initSized(w, level, o.BlockSize); err != nil {
		return nil, err
	}
	dw.d.reset(w)
	return &dw, nil
}

// SuggestBlockSize returns a BlockSize appropriate for a workload whose
// streams are typically maxStreamSize bytes.
//
// It rounds up to leave headroom for streams above the typical size and never
// returns less than 16 KiB, which measurement showed to be the knee.
func SuggestBlockSize(maxStreamSize int) int {
	// 16 KiB is the measured knee: at that size the ratio cost is nil (it was
	// fractionally *better* on real PDF content, and 8.6% faster on fonts,
	// because the working set fits cache better). At 8 KiB the cost turns
	// real: -7.2% speed and -0.76% ratio.
	const (
		minUseful = 16 << 10
		maxUseful = maxStoreBlockSize
	)
	if maxStreamSize <= 0 {
		return maxUseful
	}
	// Round up to the next power of two for allocator friendliness, with a
	// little headroom above the stated size.
	n := minUseful
	for n < maxStreamSize && n < maxUseful {
		n <<= 1
	}
	if n > maxUseful {
		n = maxUseful
	}
	return n
}
