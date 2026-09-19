# Brief for the pdfmill agent: compression, CPU and memory

You are working on **pdfmill**, a streaming Go PDF generator. This brief hands
over the results of an investigation into its compression cost. Everything here
was measured against pdfmill's own generated output — 464 sample PDFs,
12.5 GB, spanning 14 document families at 1 000 / 100 000 / 1 000 000 rows, in
plain / compressed / signed / encrypted variants.

Read this first, then `aflate/PDFMILL.md` for the integration detail.

---

## 1. Background: what was actually wrong

The starting hypothesis was that `klauspost/compress` was slow and should be
replaced with ideas from zlib-ng. **That was wrong**, and knowing why saves you
repeating it:

- klauspost's `flate` package contains **no assembly at all** — it is pure Go.
- klauspost was already **faster than both zlib-ng and libdeflate** at levels
  5–6 on this workload.

The real problem was the *shape* of pdfmill's workload. At scale, pdfmill emits
**one content stream per page, median 6.3 KiB**, and nothing else of
consequence: across the 100 000-row families that is 108 624 streams totalling
729 MB, of which **content is 99.3% of the bytes**. A million-row invoice is
140 332 pages → 140 335 near-identical ~5 KiB deflate calls.

Huffman table construction is a **per-block** cost. On a 5 KiB block it is
amortised over almost nothing — it was **27% of level-1 encode time**. That is
what got fixed, and it is why the gains grow as streams shrink.

---

## 2. Deliverable: `aflate`

A fork of `klauspost/compress/flate`, drop-in, same API, ordinary DEFLATE
output. Three changes: libdeflate-style Huffman construction, byte-granular bit
flushing, and an optional cross-stream header cache.

| level | klauspost | aflate | speed | ratio |
|---|---|---|---|---|
| L1 | 164.2 MB/s | 274.4 MB/s | **+67%** | 5.164 → 5.157 |
| L5 | 119.3 MB/s | 171.2 MB/s | **+44%** | 5.748 → 5.742 |
| L6 | 108.8 MB/s | 150.8 MB/s | **+39%** | 5.848 → 5.843 |

Plus a new `Options.BlockSize` that cuts per-writer memory **47%**, free on
page content and fonts but not on large flat images (PDFMILL.md rule 4), which
is why pdfmill does not use it.

**Your integration tasks are in `aflate/PDFMILL.md`, rules 1–9.** Summary:

1. **Use level 5**, never 7–9 (L9 costs 8× L5's CPU for 7% fewer bytes).
2. One writer per worker goroutine; `Reset` per stream (11 ns vs 32 µs to build).
3. Keep the existing channel pool — pdfmill already got this right.
4. **Keep the default block size.** `Options{BlockSize: 16 << 10}` takes a
   writer from 1048 to 560 KiB for free on content and fonts, but makes a flat
   embedded logo 6.9% larger (PDFMILL.md rule 4).
5. Budget ~1 MiB per live writer at the default block size; the GC barely
   notices them (pointer-free).
6. **Delete the 70% pre-grow** in `compressDataPooled` — real ratio is 17.2%.
7. `sliceBufferInitialSize` 16384 → 8192 (minor).
8. **Skip compression below ~150 bytes** (small streams get bigger, not smaller).
9. Don't let the destination `io.Writer` allocate.

---

## 3. The bigger opportunity is not in the compressor

Compression is now well optimised. **The generator itself is where the larger
wins are**, and they improve CPU *and* file size simultaneously rather than
trading them. Full detail with operator counts in `ENGINE_RECOMMENDATIONS.md`;
the ranked summary:

### 3a. Hoist cross-page repeated blocks into Form XObjects — biggest single win

**36.7% of all content-stream bytes are blocks that are byte-identical on more
than one page** (19.5%–84.0% depending on family): watermarks, headers,
footers, rules, table furniture.

Deflate **cannot help with any of it**. Each page is an independent deflate
stream with its own 32 KiB window, so the same watermark is re-analysed and
re-encoded on every one of 140 000 pages.

Measured: **−33.3% deflate input, −33.7% compressed bytes.**

### 3b. Stop emitting redundant graphics state

Over 854 real content streams: **66 803 `BT`, 66 803 `ET`, 66 803 `Tf`** for
**77 776 `Tj`**, plus **77 540 `Tm`** and **71 351 `rg`**. That is one text
object, one font-select and one colour-set *per text run*, and an absolute
`1 0 0 1 x y Tm` for every single show operation.

Emit state only on change, use relative `Td`, round coordinates to 2 dp and
colours to 3 dp: **−40.2% deflate input, −18.6% compressed.**

### 3c. Order matters, and it is counterintuitive

These two **do not naively compose** — de-verbosing first destroys the
byte-identity that the XObject pass depends on:

| order | deflate input | compressed |
|---|---|---|
| de-verbose only | −40.2% | −18.6% |
| XObject only | −33.3% | −33.7% |
| de-verbose then dedupe | −46.8% | −30.3% (worse) |
| **dedupe then de-verbose** | **−55.8%** | **−41.4%** |

**Extract shared blocks while the output is still verbose and regular, then
de-verbose the XObject and the per-page remainder.**

### 3d. File structure: ~25% of a large PDF is never compressed

- **Object dictionaries: 22.5% of file bytes**, plain uncompressed text. With
  280 686 objects in a 1 M-row document that is a lot of
  `<< /Type /Page /Parent 2 0 R … >>`. Fix: `/Type /ObjStm` object streams.
  (Not measured — it needs generator restructuring — but it is the largest
  untapped block of uncompressed bytes.)
- **Classic xref table: 2.3% of file bytes.** A PNG-Up-predicted cross-reference
  stream saves **5.08 MB on a 231.7 MB file** (90.4% of the xref). Standard
  PDF 1.5, universally supported. Measured on the real table:
  5 613 886 B → 536 862 B.

### 3e. Smaller items

- Merge same-line text into `TJ` arrays: **56.5% of `Tj` operators** are placed
  on the same line as the previous one, each costing a full `Tm` between them.
- Keep byte-level regularity: format numbers consistently, emit operators in a
  fixed canonical order, snap to integers when the difference is sub-pixel.
  Deflate matches *byte-identical* runs, and this is what makes 3a work.
- Cache the compressed ICC profile (409 bytes, re-deflated every document).

---

## 4. Things already correct — do not "fix" these

- **One content stream per page.** Verified 80/80 pages. Nothing to merge, and
  PDF requires per-page content to be independently decodable anyway.
- **Form XObjects left uncompressed.** Median 31 bytes; **203 of 218 sampled
  streams would get *larger* if deflated.** An earlier draft of this analysis
  recommended compressing them — that was wrong.
- **Images with no `/DecodeParms` predictor.** The obvious idea (PDF supports
  PNG's predictor via `/Predictor 15`) was measured **9.8% worse** on pdfmill's
  flat synthetic graphics, because deflate already matches whole identical
  scanlines and a predictor destroys that. Current behaviour is optimal for
  this content.
- **The channel-based writer pool.** Already correct, and for the right reason
  — `sync.Pool` drops its contents across GC cycles.

---

## 5. Measured dead ends — do not spend time here

| idea | why not |
|---|---|
| **zlib preset dictionaries** | Worth 10–18% on small streams, but **impossible in PDF**: a reader cannot be given the dictionary. Verified — `qpdf` rejects such a file (`stream inflate: data: zlib unknown error (2)`). Form XObjects (3a) are the PDF-native substitute. |
| **SIMD match-length extension** | 62% of matches are under 12 bytes; a 32-byte vector compare would idle. |
| **Smaller hash tables** (`tableBits` < 14) | Monotonically worse on **both** speed and ratio. |
| **Tuning write chunk size** | Flat from 256 B to 1 MB. Your current granularity is fine. |
| **Porting zlib-ng's match finder** | klauspost already beats zlib-ng at L5/L6 here. |
| **Cross-stream Huffman table reuse (table only)** | Measured ~0. Caching the *serialised header bits* is what pays; that is already in aflate. |

---

## 6. Compression level economics

Deflate CPU trades against bytes, and those bytes cost CPU again downstream
(AES-256 at 722 MB/s, SHA-256 at 364 MB/s, both measured) plus wire time.

| link speed | optimal level |
|---|---|
| ≤ 20 Mbit/s | L6 |
| 30–50 Mbit/s | L5 |
| 75–100 Mbit/s | L3 |
| ≥ 200 Mbit/s | L1 |

**L5 is the right default.** It is never far from optimal, and it beats L6
almost everywhere: L6 costs **10.8% more CPU for 1.36% fewer bytes**.

If output goes to fast local storage rather than a slow link, evaluate L1–L3 —
at 1 Gbit/s, L1 is ~42% cheaper in total pipeline time than L6.

`GOAMD64=v3` is a free +1–4% if you can require post-2015 CPUs.

---

## 7. How to verify anything you change

```sh
go test ./...
go test -run XXX -fuzz FuzzEncoding -fuzztime 60s ./flate/
```

And the check that actually catches bit-level bugs — round-trip real output
through **`compress/flate` from the standard library**, not through aflate's own
inflater:

```go
got, err := io.ReadAll(flate.NewReader(bytes.NewReader(compressed))) // stdlib
if err != nil || !bytes.Equal(got, original) { /* fail loudly */ }
```

A matching encoder/decoder pair can agree on a wrong bit layout. That gate
caught a real 64-bit-accumulator overflow during this work, and a hang in
`NoCompression` when the window was resized.

**Do not write tests that assert exact compressed bytes.** aflate produces
equally valid Huffman codes with different tie-breaking, so output differs from
klauspost byte-for-byte (upstream's own golden files, regenerated, came out a
net 40 bytes *smaller*).

---

## 8. Suggested order of work

1. **Swap in aflate (`aflate/zlib`, default block size)** — an afternoon, +39% encode
   throughput.
2. **Level 5 everywhere**; remove any 7–9 defaults.
3. **The `compressDataPooled` fixes** (rules 6–8) — small, safe, quick.
4. **Form XObjects for cross-page blocks** (3a) — the biggest single win,
   −33% on both axes, pure generator-side work.
5. **De-verbose the emitter** (3b), *after* 4 — takes the combined figure to
   −55.8% CPU and −41.4% bytes.
6. **Cross-reference streams** (3d) — 2.2% of file size, well-understood.
7. **Object streams** (3d) — largest remaining size win, biggest structural
   change.

Items 4 and 5 together are worth more than everything in items 1–3 combined,
and they reduce file size at the same time rather than trading against it.

---

## 9. Caveats on the evidence

- The 14 families with large variants contain **no images at all** (verified:
  0 image XObjects in all 14). The scale results describe a **pure text/table
  workload**. If production large documents embed images — especially
  photographs, which should be `DCTDecode` rather than Flate — that case is not
  represented and would need fresh samples.
- aflate's length-limited Huffman construction is **approximate** rather than
  optimal. Measured cost: +0.024% code size on adversarial synthetic frequency
  tables; on real pdfmill data the ratio came out fractionally *better*.
- The optional cross-stream header cache trades **0.17% ratio for ~5% speed**.
  Disable with `warmHeaderCache = false` in `flate/warmtable.go` if that is the
  wrong trade for you.
