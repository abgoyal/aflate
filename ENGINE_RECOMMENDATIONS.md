# PDF engine: what to change, ranked by measured impact

Everything here is measured against the uploaded samples (14 document families
× {1 000, 100 000, 1 000 000} rows × {plain, C, S, E, CS, CE, SE, CSE}),
not estimated. Where a number is an estimate or an untested idea it says so.

At scale the workload is essentially **one ~6 KB content stream per page,
nothing else**: 108 624 deflate-fed streams across 95 653 pages, 729 MB
decoded, of which content is **99.3% of bytes**. Fonts, images, ICC and
metadata are rounding errors above the 1 000-row scale.

The two big levers below are worth far more than the compression patch.

---

## Tier 1 — the two changes that matter

### 1. Hoist cross-page repeated blocks into Form XObjects

**36.7% of all content-stream bytes are blocks that are byte-identical on more
than one page** (per family: 19.5% to **84.0%**). These are watermarks, headers,
footers, rules, repeated table furniture.

Deflate cannot help with any of it. Each page is an independent deflate stream
with its own 32 KiB window, so the same bytes are re-analysed and re-encoded on
every page. Compressing a 140 000-page document re-compresses the same
watermark 140 000 times.

The `statement` family shows the pattern plainly — a tiled watermark, each tile
a full nine-line block, repeated across the page and on every page:

```
q
1 0 0 1 -792 -792 cm
0.866 -0.5 0.5 0.866 0 0 cm
BT
/F2 28 Tf
-59.906 -9.3333 Td
(User test) Tj
ET
Q
```

Define shared blocks once as a Form XObject and invoke them with `/X0 Do`.
Simulated over the real corpus:

| | deflate input (CPU) | compressed bytes |
|---|---|---|
| today | 5 910 198 | 1 003 746 |
| with Form XObjects | 3 942 299 (**−33.3%**) | 665 300 (**−33.7%**) |

A tiled watermark specifically should be a **Pattern** or a single XObject
invoked with a `cm` per tile, not N copies of the drawing operators.

### 2. Stop emitting redundant graphics state

Operator counts over 854 real content streams: **66 803 `BT`, 66 803 `ET`,
66 803 `Tf`** for **77 776 `Tj`**, plus **77 540 `Tm`** and **71 351 `rg`**.

That is one text object, one font-select and one colour-set *per text run*, and
an absolute `1 0 0 1 x y Tm` for every single show operation.

Four fixes, all safe:

- **One `BT`/`ET` per run of text**, not per fragment.
- **Only emit `Tf` when the font or size actually changes.** Nearly all 66 803
  are re-selecting a font that is already current.
- **Only emit `rg`/`RG`/`w` on change.**
- **Use relative `Td` instead of absolute `1 0 0 1 x y Tm`.** The `1 0 0 1`
  prefix is 8 wasted bytes on every one of 77 540 placements.
- **Round coordinates to 2 dp and colours to 3 dp.** `597.4`, `100.138`,
  `0.1725 0.2431 0.3137` — 2 dp at 72 dpi is sub-micron, and colours are 8-bit
  downstream so beyond 3 dp is noise.

Measured: **−40.2% deflate input, −18.6% compressed.**

### Order matters: dedupe first, then de-verbose

These two do **not** naively compose — de-verbosing removes the very `Tf`/`rg`
lines that make blocks byte-identical across pages, so doing it first destroys
about a third of the XObject opportunity:

| order | deflate input | compressed |
|---|---|---|
| de-verbose only | −40.2% | −18.6% |
| XObject only | −33.3% | −33.7% |
| de-verbose then dedupe | −46.8% | −30.3% (worse!) |
| **dedupe then de-verbose** | **−55.8%** | **−41.4%** |

**Extract shared blocks while the output is still verbose and regular, then
de-verbose the XObject and the per-page remainder.**

Combined with the compression patch (+30% throughput at L6), content-stream
deflate CPU drops to roughly **one third of today's**.

---

## Tier 2 — file structure

### 3. Use cross-reference streams (PDF 1.5)

Every file uses a classic uncompressed xref table. On `invoice-1000000-C.pdf`
(231.7 MB, 280 686 objects) that table is **5.61 MB = 2.42% of the file**,
stored as ASCII and never compressed.

Measured on the real table:

| form | bytes |
|---|---|
| ASCII xref table (today) | 5 613 886 |
| deflated as-is | 1 118 559 |
| binary xref stream `/W [1 4 2]` | 1 964 802 |
| + deflated | 1 050 892 |
| **+ PNG-Up predictor, deflated** | **536 862** |

**Saves 5.08 MB — 90.4% of the xref, 2.19% of the whole file.** The PNG-Up
predictor matters: it turns monotonically increasing byte offsets into small
deltas. This is a standard PDF 1.5 feature supported everywhere.

### 4. Use object streams (ObjStm) — the largest remaining size lever

**22.5% of a large C-variant file is object dictionaries stored as plain
uncompressed text** (40.26 MB of 179.24 MB across the 100 000-row families).
With 280 686 objects in a 1 M-row document, that is a lot of
`<< /Type /Page /Parent 2 0 R /Contents 5 0 R ... >>`.

Batching non-stream objects into `/Type /ObjStm` puts all of it through
deflate. This structure is extremely repetitive, so it should compress very
well — I have **not measured it** (it needs the generator to restructure the
file, not something I can simulate from the output), but it is the biggest
untapped block of uncompressed bytes in the file.

Together, items 3 and 4 address roughly **25% of the file that is currently
never compressed at all**.

### 5. Small items

- **Leaving the 12 921 Form XObjects uncompressed is correct** — do not
  "fix" this. Their median size is **31 bytes**, and **203 of 218 sampled
  streams would get *larger* if deflated**, because a zlib header plus a block
  header exceeds the payload. An earlier draft of this document recommended
  compressing them; that was wrong, and measurement corrected it. If anything,
  apply the same rule elsewhere: **skip compression below ~150 bytes**.
- **The XMP metadata stream is uncompressed** in every variant (~0.8 KB/doc).
  At that size compression is roughly break-even; not worth changing.
- **The 409-byte ICC profile is re-deflated in every document.** Cache the
  compressed bytes once. Trivial, but free.

---

## Tier 3 — help the compressor help you

### 6. Merge same-line text into `TJ` arrays

**56.5% of `Tj` operators are placed on the same line as the previous one**
(3 fragments per line is the most common case, and runs of 5 and 9 occur).
Each currently costs a full `Tm`/`Td` between fragments.

```
BT /F1 11 Tf 0 0 0 rg 1 0 0 1 50 597.4 Tm (Invoice #: ) Tj
1 0 0 1 100.138 597.4 Tm (INV-001) Tj ET
```

becomes one show operation with the gap expressed as kerning:

```
BT /F1 11 Tf 50 597.4 Td [(Invoice #: ) -4500 (INV-001)] TJ ET
```

This is partly counted in item 2 already; the incremental win is eliminating
the intermediate placements entirely. Estimated, not separately measured.

### 7. Keep byte-level regularity

Deflate finds *byte-identical* matches. Anything that makes two logically
identical constructs differ byte-wise costs both ratio and CPU:

- **Format numbers consistently.** `50` vs `50.0` vs `50.00` for the same
  value breaks matches. Pick one representation and always use it.
- **Emit operators in a fixed canonical order** within a block, so repeated
  rows differ only in their data.
- **Snap to integers when the difference is sub-pixel** — shorter *and* more
  repetitive.
- **Keep resource names short and stable** (`/F1`, not regenerated per page).

This is what makes item 1 work: the more byte-identical your repeated blocks
are, the more of them the XObject pass can hoist.

### 8. Do not bother with these

Measured and rejected, so nobody repeats the work:

- **zlib preset dictionaries** — worth 10.1% overall and 18.2% on sub-1 KB
  streams, but **impossible in PDF**. A reader has no way to be given the
  dictionary; qpdf rejects such a file outright (`stream inflate: data: zlib
  unknown error (2)`). Form XObjects (item 1) are the PDF-native substitute.
- **Merging page content streams** — every page already emits exactly one
  content stream (checked, 80/80 pages). Nothing to merge, and PDF requires
  per-page content to be independently decodable anyway.
- **Changing write chunk size into the compressor** — flat from 256 B to 1 MB.

---

## Images — corrected guidance

The large-variant families contain **no images at all** (verified: 0 image
XObjects in all 14 families at 100 000 rows). Images appear only in the small
demo families — `demo-image-demo` (24) and `demo-qr-demo` (2) — which have no
large variants. So the scale results above are a pure text/table workload; if
production large documents carry images, that case is **not represented in
this corpus**.

What the image streams that do exist look like: 200x200 `/DeviceRGB` and
`/DeviceGray` raw bitmaps, `/FlateDecode`, **no `/DecodeParms`**, decoded from
source PNGs. 24 images, 1.92 MB raw, compressing to 164 601 B (ratio 11.66).

### Do NOT add PNG predictors here

The obvious idea — PDF supports PNG's own predictor via
`/DecodeParms << /Predictor 15 ... >>`, so use it — is **wrong for this
content**, measured:

| | bytes | ratio |
|---|---|---|
| deflate, no predictor (today) | 164 601 | 11.66 |
| deflate + PNG optimal predictor | 180 659 | 10.63 |

**9.8% worse.** These are synthetic graphics: flat colour fields and sharp
edges. Without a predictor, deflate matches *entire identical scanlines* at
distance = stride, producing very long matches. A predictor replaces those
exact repeats with per-row filter bytes and residuals and destroys the
matching. Predictors pay for photographic content, where exact repeats do not
occur but local gradients do — not for flat art.

**Current behaviour is already optimal for this image type.** Leave it alone.

### Two things that would help, by image type

- **Photographs → `DCTDecode` (JPEG), not Flate.** The library already supports
  JPEG; these samples just do not exercise it. Flate on photographic data gives
  a ratio near 1.1, versus 10-20x for JPEG at visually equivalent quality. If
  any production documents embed photos, this is the single biggest image win
  available and it is a configuration choice, not new code.
- **PNG passthrough for CPU.** When a source PNG's colour type and bit depth map
  directly onto a PDF colour space, its `IDAT` is *already* a deflate stream
  with predictors applied, and can be copied into the PDF verbatim with
  `/Predictor 15` — skipping both the PNG decode and the re-deflate entirely.
  On this corpus that would cost about 10% in image bytes (per the table above)
  but eliminate 100% of image compression CPU. Worth it for image-heavy
  documents; not worth it if images are few.


---

## Tier 4 — compression settings

- **Apply the patch.** +30% at L6, +54% at L1 on the large-document corpus,
  ratio unchanged. See `FINDINGS.md`.
- **Never use levels 7–9.** L9 costs 3.3× L6's CPU for 2.9% fewer bytes and is
  optimal at no link speed.
- **Pick the level from your link speed** (output encrypted and signed):
  ≤20 Mbit/s → L6; 30–50 → L5; 75–100 → L3; ≥200 Mbit/s → L1. The L1–L6 band
  is within ~2% of optimal throughout, so **L5 is a safe default**.
- **`GOAMD64=v3`** is +1–4% for free if you can require post-2015 CPUs.
- **Memory**: ~1 MB retained per pooled writer (262 KB token array, 320 KB
  history, 384 KB hash tables). Two one-line constants take that to ~670 KB;
  see `FINDINGS.md` §6.

---

## Suggested order of work

1. **Form XObjects for cross-page blocks** — biggest single win, −33% on both
   axes, and it is pure generator-side work.
2. **De-verbose the emitter** (after 1) — takes the combined figure to −55.8%
   CPU and −41.4% bytes.
3. **Apply the compression patch** — independent of both, +30%.
4. **Cross-reference streams** — 2.19% of file, well-understood, low risk.
5. **Object streams** — likely the largest remaining size win, but the biggest
   structural change.
6. Level and build settings — minutes of work, small but free.
