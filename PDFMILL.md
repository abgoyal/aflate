# aflate — integration guide for the pdfmill agent

Read this before changing any compression code in pdfmill.

## What this is

`aflate` is a fork of `klauspost/compress/flate` tuned for pdfmill's workload:
**many small independent streams**, one per PDF page content stream, compressed
through a pooled `Writer` that is `Reset` between streams.

It is a drop-in replacement. Same API, same package name, ordinary DEFLATE
output that any PDF reader decodes.

```go
import "github.com/abgoyal/aflate/flate"

w, err := flate.NewWriter(dst, 5)
```

Measured on 854 real pdfmill page content streams (median 6.3 KB):

| level | klauspost | aflate | speed | ratio |
|---|---|---|---|---|
| L1 | 164.2 MB/s | 274.4 MB/s | **+67%** | 5.164 → 5.157 |
| L5 | 119.3 MB/s | 171.2 MB/s | **+44%** | 5.748 → 5.742 |
| L6 | 108.8 MB/s | 150.8 MB/s | **+39%** | 5.848 → 5.843 |

The gain grows as streams get smaller, because what was optimised is
*per-block* cost — at 512-byte streams it is over +50%.

## Rules

### 1. Use level 5

Not 6. On pdfmill's own corpus L6 costs **10.8% more CPU to save 1.36% of
bytes**. L5 is closer to optimal than L6 at every link speed except below
~20 Mbit/s, where L6 wins by about 1% of total pipeline time.

**Never use 7, 8 or 9.** L9 costs **8× L5's CPU** for 7% fewer bytes and is
optimal nowhere. If any code path or config default selects 7–9, change it.

If output goes to fast local storage rather than a slow link, L1–L3 are worth
evaluating: at 1 Gbit/s, L1 is ~42% cheaper in total pipeline time than L6.

### 2. One writer per worker goroutine, never one per stream

Constructing a writer costs **32.3 µs and 735 KiB across 12 allocations**.
`Reset` costs **11 ns and allocates nothing**. That is a ~3000× difference.

```go
w := pool.Get().(*flate.Writer)
defer pool.Put(w)
w.Reset(dst)          // <- per stream
_, err := w.Write(content)
err = w.Close()       // required: flushes the final block
```

Steady-state encoding is **0 allocs/op**. If an alloc profile shows otherwise,
the allocation is coming from the destination writer or the caller, not aflate.

### 3. Pick the pool type by workload shape — this matters more than it looks

`sync.Pool` discards its contents on every GC cycle (a victim cache buys one
more). Measured:

| workload | sync.Pool | bounded free-list |
|---|---|---|
| continuous load | 12 writers built | 12 writers built |
| bursty (idle across ≥2 GC cycles between jobs) | **48 built, 34.5 MiB churned** | 12 built, 8.6 MiB |

Under continuous load `sync.Pool` is fine — keep it. If pdfmill generates a
document then idles (a request-driven service, a queue worker between jobs),
every burst pays a full rebuild of the pool: 12 × 735 KiB of allocation and
12 × 32 µs of construction, for nothing.

For bursty use, hold strong references instead:

```go
type writerPool struct{ ch chan *flate.Writer }

func newWriterPool(n, level int) *writerPool {
    return &writerPool{ch: make(chan *flate.Writer, n)}
}

func (p *writerPool) get(level int) *flate.Writer {
    select {
    case w := <-p.ch:
        return w
    default:
        w, _ := flate.NewWriter(io.Discard, level)
        return w
    }
}

func (p *writerPool) put(w *flate.Writer) {
    select {
    case p.ch <- w:
    default: // over capacity, let it be collected
    }
}
```

**Bound it.** Size the channel to your worker count (typically `GOMAXPROCS`),
not unbounded — each entry is ~1 MiB of resident memory.

### 4. Budget ~1.05 MiB per live writer, and know that the GC does not care

Per-writer footprint by level:

| level | KiB |
|---|---|
| L1 | 806 |
| L4 | 934 |
| **L5 / L6** | **1062** |
| L2 / L3 | 1190 (largest — avoid if memory-bound) |
| L9 | 1130 |

It breaks down as ~262 KiB token array, ~320 KiB history buffer, ~384 KiB hash
tables, ~85 KiB other. aflate adds 4.4 KiB (+0.4%) over klauspost.

**The important part:** these structures are pointer-free, so Go allocates them
into noscan spans and the collector never traverses them. Measured marginal GC
cost of holding writers:

| live writers | heap | added GC cycle time |
|---|---|---|
| 12 | 12.4 MiB | +7 µs (noise) |
| 64 | 65.9 MiB | +186 µs |
| 256 | 263.8 MiB | +498 µs |

256 writers is a quarter-gigabyte of heap for half a millisecond of mark time.
So the instinct "more retained heap beats allocation churn" is **correct here**:
holding writers is cheap for the GC, and retained heap also raises the GC
trigger point, making collections less frequent.

What actually hurts is the opposite pattern — many small short-lived
allocations. Spend effort there, not on shrinking the writer pool.

### 5. Do not let the destination allocate

aflate writes into whatever `io.Writer` you hand `Reset`. If that is a
`bytes.Buffer` that grows from zero, you have moved the allocation rather than
removed it. Either write straight to the file/socket, or pool pre-sized
buffers. A page content stream compresses to roughly 1–2 KiB, so a 4 KiB
pre-sized buffer covers almost all of them without regrowth.

## Two knobs if peak RSS becomes the binding constraint

Both are one-line constants in `flate/`, both measured, neither is on by
default because the right choice depends on stream sizes.

- `allocHistory` in `fast_encoder.go`, `maxStoreBlockSize * 5` → `* 2`:
  saves **192 KiB per writer**. Free (slightly positive) for streams under
  ~64 KiB, costs ~3.5% at L1 on multi-megabyte streams. pdfmill's streams are
  ~5 KiB, so this is close to free.
- `tableBits` in `fast_encoder.go`, `15` → `14`: saves another **192 KiB** at
  L5/L6 for −0.2% speed and −0.06% ratio.

Together these take an L5 writer from ~1062 KiB to ~680 KiB. Do not go below
`tableBits = 14`; 11–13 were measured and are worse on **both** speed and
ratio.

## Runtime settings

- **`GOMEMLIMIT`** is the right backstop for a pooled design: it lets the heap
  grow (fewer GCs) but caps RSS. Prefer it over lowering `GOGC`.
- **Do not lower `GOGC`** to control memory. It increases GC frequency, which is
  exactly what makes `sync.Pool` drop writers (rule 3).
- **`GOAMD64=v3`** is worth +1–4% for free if you can require post-2015 CPUs
  (AVX2/BMI2). It is a build-time environment variable, no code change.

## What NOT to do

Each of these was measured and rejected; do not spend time re-deriving them.

- **zlib preset dictionaries.** Worth 10–18% on small streams, but **impossible
  in PDF** — a reader cannot be handed the dictionary and the file fails to
  inflate (`qpdf` rejects it outright). Use Form XObjects instead.
- **Merging page content streams.** pdfmill already emits exactly one per page,
  and PDF requires each to be independently decodable.
- **Tuning the write chunk size** into the compressor. Flat from 256 B to 1 MB.
- **SIMD match-length extension.** 62% of matches are under 12 bytes; a 32-byte
  vector compare would idle.
- **PNG predictors on image XObjects.** Measured **9.8% worse** on pdfmill's
  flat synthetic graphics, because deflate already matches whole identical
  scanlines. (Predictors would help photographs — but photographs should be
  `DCTDecode`, not Flate, in the first place.)

## The bigger win is not in the compressor

Compression is now well optimised. The larger opportunities are in what
pdfmill *emits*, measured on its own output:

1. **36.7% of content-stream bytes are byte-identical blocks repeated across
   pages** (watermarks, headers, footers, rules). Deflate cannot help — each
   page is a separate stream with its own window, so the same watermark is
   re-compressed on every one of 140 000 pages. Hoisting them into Form
   XObjects: **−33% CPU and −34% bytes**.
2. **Redundant graphics state.** 66 803 `BT`/`ET`/`Tf` for 77 776 `Tj`, plus an
   absolute `1 0 0 1 x y Tm` on every single show operation. Emitting state only
   on change and using relative `Td`: **−40% CPU, −19% bytes**.
3. **Order matters and is counterintuitive**: de-verbosing *first* destroys the
   byte-identity that step 1 relies on. Dedupe into XObjects first, then
   de-verbose: **−55.8% CPU, −41.4% bytes**.
4. ~25% of a large PDF is never compressed at all — object dictionaries (needs
   `ObjStm`) and the classic xref table (a PNG-Up-predicted xref stream saves
   5.08 MB on a 232 MB file).

See `ENGINE_RECOMMENDATIONS.md` for the full ranked list with operator counts.

## Verification you must run after touching this

```sh
go test ./...                                              # upstream suite
go test -run XXX -fuzz FuzzEncoding -fuzztime 60s ./flate/ # 2345-entry corpus
```

And the check that actually catches bit-level bugs: round-trip real output
through **`compress/flate` from the standard library**, not through aflate's own
inflater. A matching encoder/decoder pair can agree on a wrong bit layout — that
is exactly how an accumulator-overflow bug was caught during this work.

```go
got, err := io.ReadAll(flate.NewReader(bytes.NewReader(compressed))) // stdlib
if err != nil || !bytes.Equal(got, original) { /* fail loudly */ }
```

## One caveat to be aware of

aflate's length-limited Huffman construction is **approximate** rather than
optimal (libdeflate's method; upstream uses exact package-merge). Measured cost
is +0.024% code size on adversarial synthetic frequency tables, and on real
pdfmill data the ratio came out fractionally *better*. It only matters if the
15-bit code-length limit binds, which does not happen for DEFLATE's alphabets
in practice. Byte-for-byte output therefore differs from klauspost — do not
write tests that assert exact compressed bytes.

Optionally, `warmHeaderCache = false` in `flate/warmtable.go` disables the
cross-stream header cache, trading ~5% speed to recover ~0.17% ratio.
