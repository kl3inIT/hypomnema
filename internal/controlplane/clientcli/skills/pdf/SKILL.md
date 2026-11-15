---
name: pdf
description: "PDF files: create, read, merge, fill."
license: MIT
---

# PDF Skill

Create PDFs from structured specs, build and fill AcroForm forms (with layout linting and visual overlays), extract text/tables/metadata, merge/split/rotate/watermark/stamp pages, export page images, manage metadata and attachments, and encrypt/decrypt — using pypdf, reportlab, and pdfplumber.

## When to Use

- Generate a report, invoice, or multi-page document as PDF.
- Build a fillable AcroForm (text/checkbox/radio/dropdown) from a JSON spec, linting the layout first.
- Pull text, tables (JSON/CSV), metadata, or form-field values out of a PDF.
- Merge, split, rotate, extract page subsets, watermark, stamp text/images at coordinates, bookmark, or compress PDFs.
- Export pages as PNGs for visual review; set/clear document metadata; add/extract file attachments.
- Fill or flatten AcroForm forms; encrypt or decrypt with passwords.
- NOT for scanned/image-only PDFs and NOT for pixel-perfect HTML-to-PDF rendering (use a headless browser).

## Prerequisites

- Python 3.10+ with `pypdf`, `reportlab`, `pdfplumber`.
- The helpers are in `scripts/` beside this SKILL.md. Set `S` to that folder's
  absolute path (this skill's folder, the one SKILL.md is in, then `/scripts`):

  ```bash
  S="<this skill's folder>/scripts"
  python -c "import pypdf, reportlab, pdfplumber" && python "$S/pdf_create.py" --help
  ```

- In a Bot's box `python` is the documents environment, first on PATH, which
  already imports the libraries. Never `pip install` into it; it is read-only.
- Elsewhere (a worker on a node), when that check fails, put one environment
  with the same pinned versions first on PATH; it is made once in the home and
  is safe to run from several agents at once:

  ```bash
  python -c "import pypdf, reportlab, pdfplumber" 2>/dev/null || (
    # Each caller builds a private venv; a symlink publishes the first finished one atomically, the others
    # delete theirs; a published build is replaced only while it still fails to import.
    mkdir -p "$HOME/.cache" && cd "$HOME/.cache" && stage="$(mktemp -d hypomnema-docs-venv.XXXXXX)" || exit 1
    old="$(readlink hypomnema-docs-venv)"
    python3 -m venv "$stage" &&
      "$stage/bin/python" -m pip install -q --disable-pip-version-check python-docx==1.2.0 openpyxl==3.1.5 python-pptx==1.0.2 reportlab==5.1.0 pypdf==6.21.4 pdfplumber==0.11.10 pypdfium2==5.14.0 &&
      "$stage/bin/python" -c "import docx, openpyxl, pptx, reportlab, pypdf, pdfplumber, pypdfium2" &&
      if [ -z "$old" ]; then python3 -c 'import os, sys; os.symlink(*sys.argv[1:])' "$stage" hypomnema-docs-venv
      elif [ "$(readlink hypomnema-docs-venv)" = "$old" ] && ! "$old/bin/python" -c "import docx, openpyxl, pptx, reportlab, pypdf, pdfplumber, pypdfium2" 2>/dev/null; then
        ln -s "$stage" "$stage.lnk" && python3 -c 'import os, sys; os.replace(*sys.argv[1:])' "$stage.lnk" hypomnema-docs-venv &&
          case "$old" in hypomnema-docs-venv.??????) rm -rf "$old" ;; esac
      fi 2>/dev/null
    [ "$(readlink hypomnema-docs-venv)" = "$stage" ] || rm -rf "$stage" "$stage.lnk"
  ) && export PATH="$HOME/.cache/hypomnema-docs-venv/bin:$PATH"
  ```

  Run every helper below with `python`.
- Page rasterization (`pdf_page_image.py`, overlay rendering) uses `pypdfium2`, which the documents environment already has, or poppler's `pdftoppm` on PATH. Scripts fall back pypdfium2 → pdftoppm and report `{"rendered": false, "missing": [...]}` (exit 0) when neither exists.
- Each helper script checks imports lazily and prints an install hint if a dependency is missing.

## How to Run

All helpers live in `scripts/` (`$S` above) and are argparse CLIs — run them with the `bash` tool; every one supports `--help`. They read/write JSON strictly as UTF-8, print JSON results to stdout, and exit non-zero on failure. `pdf_read.py` takes exactly one of `--text`, `--tables`, `--meta`, `--fields` per run; for text and metadata run it twice.

```bash
python "$S/pdf_create.py" spec.json -o out.pdf         # build PDF from JSON spec
python "$S/pdf_make_form.py" formspec.json -o form.pdf # build fillable AcroForm from JSON spec
python "$S/pdf_form_layout.py" formspec.json           # lint form layout BEFORE building
python "$S/pdf_form_layout.py" formspec.json --render-overlay boxes.png [--pdf form.pdf]
python "$S/pdf_read.py" doc.pdf --text                 # per-page text (JSON)
python "$S/pdf_read.py" doc.pdf --tables --csv-dir t/  # tables to JSON + CSV files
python "$S/pdf_read.py" doc.pdf --meta                 # metadata, page sizes, encrypted/scanned flags
python "$S/pdf_read.py" form.pdf --fields              # form fields: name, type, value
python "$S/pdf_merge.py" a.pdf b.pdf -o merged.pdf [--bookmarks]
python "$S/pdf_split.py" doc.pdf --pages 1-3,7 -o part.pdf [--rotate 90]
python "$S/pdf_fill_form.py" form.pdf --fields-json values.json -o filled.pdf [--flatten]
python "$S/pdf_secure.py" doc.pdf --encrypt -o enc.pdf --user-password your-password
python "$S/pdf_secure.py" enc.pdf --decrypt -o dec.pdf --password your-password
python "$S/pdf_watermark.py" doc.pdf --stamp mark.pdf -o stamped.pdf [--under]
python "$S/pdf_stamp.py" doc.pdf -o out.pdf --text "DRAFT" --x 150 --y 400 \
    --font-size 60 --rotation 45 --opacity 0.3 --color "#cc0000" [--pages 1-3]
python "$S/pdf_stamp.py" doc.pdf -o out.pdf --image sig.png --x 400 --y 60 --width 120
python "$S/pdf_page_image.py" doc.pdf --pages 1-3 --dpi 150 --out-dir imgs/
python "$S/pdf_meta.py" doc.pdf --set-meta --title "T" --author "A" -o out.pdf
python "$S/pdf_meta.py" doc.pdf --attach data.csv -o out.pdf
python "$S/pdf_meta.py" doc.pdf --list-attachments | --extract-attachments dir/
```

## Quick Reference

| Task | Tool | Command / API |
|---|---|---|
| Create doc (headings, tables, images) | reportlab platypus | `pdf_create.py spec.json -o out.pdf` |
| Build fillable form | reportlab acroForm | `pdf_make_form.py formspec.json -o form.pdf` |
| Lint form layout / overlay image | pure python + PIL | `pdf_form_layout.py formspec.json [--render-overlay o.png]` |
| Per-page text | pdfplumber | `pdf_read.py f.pdf --text` |
| Tables → JSON/CSV | pdfplumber | `pdf_read.py f.pdf --tables` |
| Metadata / sizes / encrypted / scanned | pypdf + pdfplumber | `pdf_read.py f.pdf --meta` |
| Merge (+ outline) | pypdf | `pdf_merge.py a.pdf b.pdf -o m.pdf` |
| Split / extract / rotate | pypdf | `pdf_split.py f.pdf --pages 2-5 --rotate 90` |
| List / fill / flatten form | pypdf | `pdf_read.py --fields`, `pdf_fill_form.py` |
| Encrypt / decrypt (AES-256) | pypdf | `pdf_secure.py --encrypt/--decrypt` |
| Watermark / stamp PDF page | pypdf | `pdf_watermark.py f.pdf --stamp w.pdf` |
| Stamp text/image at coordinates | reportlab + pypdf | `pdf_stamp.py f.pdf --text "Sign here" --x 400 --y 60` |
| Pages → PNG (review) | pypdfium2 or pdftoppm | `pdf_page_image.py f.pdf --pages 1-3 --out-dir imgs/` |
| Set/clear metadata, attachments | pypdf | `pdf_meta.py --set-meta / --attach / --extract-attachments` |
| Compress content streams | pypdf | `pdf_split.py f.pdf --pages 1-N --compress` |

## Procedure

1. **Inspect first.** Run `pdf_read.py file.pdf --meta`. Check `encrypted` (if true, decrypt first with `pdf_secure.py --decrypt`) and `likely_scanned_pages`. If pages are image-only, export them with `pdf_page_image.py --pages <scanned> --dpi 300 --out-dir imgs/` and look at the PNGs with `read` — do not report empty text as "no content".
2. **Create.** Write a JSON spec with `write` (elements: `heading`, `paragraph`, `table`, `image`, `pagebreak`; optional `title`/`author` metadata; `page_size` is `"A4"` or `"letter"`; page numbers are added automatically, `"page_numbers": false` turns them off), then run `pdf_create.py`. Verify visually with `read` on a rendered page image if layout matters. Text outside Latin-1, such as Vietnamese, needs a TTF font: see "Vietnamese and other non-Latin text" below.

   ```json
   {
     "title": "Example Report",
     "author": "example-author",
     "page_size": "A4",
     "elements": [
       {"type": "heading", "text": "Section 1", "level": 1},
       {"type": "paragraph", "text": "Body text..."},
       {"type": "table", "rows": [["H1", "H2"], ["a", "b"]], "header": true},
       {"type": "image", "path": "chart.png", "width": 400},
       {"type": "pagebreak"}
     ]
   }
   ```
3. **Extract.** `--text` gives a JSON list of per-page strings; `--tables` gives row arrays per page and can also emit CSV files. Read results with `read`; never eyeball a binary PDF directly.
4. **Manipulate.** `pdf_merge.py` concatenates and can add one bookmark per source file; `pdf_split.py` handles page ranges (1-based, e.g. `1-3,5,9-`), rotation in 90° steps, and `--compress`. Watermark by preparing a single-page stamp PDF (e.g. via `pdf_create.py`) and overlaying it with `pdf_watermark.py`; for one-liner stamps ("sign here", diagonal DRAFT, corner labels) use `pdf_stamp.py` with text or an image at explicit coordinates.
5. **Build forms.** Write one form-spec JSON (fields with `label_box`/`entry_box` in PDF points — see `references/forms.md`), lint it with `pdf_form_layout.py` and fix every reported problem, optionally review the `--render-overlay` PNG with `read`, then build with `pdf_make_form.py` and confirm with `pdf_read.py --fields`.
6. **Fill forms.** List fields (`--fields`) to learn exact names and types, write a UTF-8 JSON of `{"FieldName": "value"}` with `write` (checkboxes accept `true`/`false`; radio/choice values must match the field's export options), then `pdf_fill_form.py`. Re-read with `--fields` to confirm values landed.
7. **Metadata & attachments.** `pdf_meta.py --set-meta` writes Title/Author/Subject/Keywords (DocInfo); `--clear-meta` drops them; `--attach`/`--list-attachments`/`--extract-attachments` round-trip embedded files.
8. **Secure.** Encrypt with distinct user/owner passwords and AES-256. To remove a password you know, `--decrypt` writes an unencrypted copy.
9. **Verify** (see below) before reporting success.

## Vietnamese and other non-Latin text

reportlab's built-in fonts (Helvetica, which `pdf_create.py`, `pdf_make_form.py` and `pdf_stamp.py` draw with) have no glyphs outside Latin-1: Vietnamese letters such as "ư", "ạ", "ố" come out as black boxes or nothing. For such text, register a TTF that covers it and write the document with reportlab directly. DejaVu Sans covers Vietnamese; check it is there first:

```bash
F=/usr/share/fonts/truetype/dejavu
test -f "$F/DejaVuSans.ttf" && test -f "$F/DejaVuSans-Bold.ttf" && echo ok || fc-list ':lang=vi' file
```

If it is missing, `fc-list ':lang=vi' file` lists the fonts that cover Vietnamese; use one of those paths. If none is listed, say so; do not deliver a PDF whose letters are boxes.

```python
from reportlab.lib import colors
from reportlab.lib.pagesizes import A4
from reportlab.lib.styles import getSampleStyleSheet
from reportlab.pdfbase import pdfmetrics
from reportlab.pdfbase.ttfonts import TTFont
from reportlab.platypus import Paragraph, SimpleDocTemplate, Table, TableStyle

F = "/usr/share/fonts/truetype/dejavu"
pdfmetrics.registerFont(TTFont("DejaVuSans", f"{F}/DejaVuSans.ttf"))
pdfmetrics.registerFont(TTFont("DejaVuSans-Bold", f"{F}/DejaVuSans-Bold.ttf"))
pdfmetrics.registerFontFamily("DejaVuSans", normal="DejaVuSans", bold="DejaVuSans-Bold")

styles = getSampleStyleSheet()
heading, body = styles["Heading1"], styles["BodyText"]
heading.fontName, body.fontName = "DejaVuSans-Bold", "DejaVuSans"

table = Table([["Miền", "Doanh thu"], ["Bắc", "120"]], repeatRows=1)
table.setStyle(TableStyle([
    ("FONTNAME", (0, 0), (-1, -1), "DejaVuSans"),
    ("FONTNAME", (0, 0), (-1, 0), "DejaVuSans-Bold"),
    ("GRID", (0, 0), (-1, -1), 0.5, colors.grey),
]))
SimpleDocTemplate("out.pdf", pagesize=A4, title="Doanh thu").build(
    [Paragraph("Doanh thu ba miền", heading), Paragraph("Đơn vị: triệu đồng.", body), table])
```

Then check both the text (`pdf_read.py out.pdf --text` shows the diacritics) and the look (`pdf_page_image.py` and `read`): extracted text can be right while the page shows boxes.

## Pitfalls

- **Scanned PDFs**: empty `extract_text()` plus page images means there is no text layer. Look at its page images (`pdf_page_image.py`) instead; do not fabricate text.
- **Flattening limits**: `pdf_fill_form.py --flatten` uses pypdf's flatten support, which converts widget appearances into page content. It is reliable for plain text fields and checkboxes but can drop or misrender exotic widgets (rich text, custom appearance streams, some radio groups). Verify the flattened output visually with `read`; for bulletproof flattening use an external renderer (e.g. Ghostscript or `pdftoppm`+reassembly) as a fallback.
- **NeedAppearances**: after filling, viewers only render values if appearance streams exist. The fill script sets the AcroForm `NeedAppearances` flag so conforming viewers regenerate them; some minimal viewers ignore it — flatten if display fidelity matters.
- **Non-Latin form values**: values are stored correctly (UTF-16), but the field's default font may lack glyphs, so a viewer can show blanks even though the data round-trips. Verify with `--fields`, not just visually.
- **Compression expectations**: `--compress` only deflates content streams. Typical savings are 0–20%; it does nothing for PDFs dominated by images or already-compressed streams. It is not a substitute for image downsampling (Ghostscript territory).
- **Permission flags don't enforce**: owner-password permission bits (no-print, no-copy) are polite requests that viewers may honor; any library (including pypdf) can read and strip them. Only the user password actually gates content via encryption. Never present permission flags as security.
- **Table extraction is heuristic**: pdfplumber detects tables from ruling lines/word alignment; borderless or merged-cell tables may need `table_settings` tuning or manual cleanup.
- **Page indexing**: helper CLIs take 1-based pages; pypdf APIs are 0-based. The scripts convert — don't double-convert.
- **Rotated stamp text extraction**: pdfplumber's line grouping scrambles rotated glyphs (a 45° "DRAFT" extracts as stray letters); verify rotated stamps with `pypdf`'s `extract_text()` or a rendered image instead.
- **Radio groups**: reportlab needs ≥2 `radio()` widgets per group, fills need the slashed export value (`"/red"`), and flatten fidelity is worst for radios — see `references/forms.md`.
- **Metadata scope**: `pdf_meta.py` writes the classic DocInfo dictionary only; embedded XMP metadata (if any) is left untouched and may show different values in some viewers.
- **PDF/A is out of scope**: pypdf/reportlab cannot produce or validate conformant PDF/A. If archival conformance is required, run Ghostscript via the `bash` tool (e.g. `gs -dPDFA=2 -dPDFACompatibilityPolicy=1 -sColorConversionStrategy=UseDeviceIndependentColor -sDEVICE=pdfwrite -o out.pdf in.pdf` with a suitable ICC profile) and validate with veraPDF — both are external installs, and the result still needs validation, not assumption.
- Rotation must be a multiple of 90; encrypted inputs must be decrypted before any other operation.

## Verification

- After create/merge/split: `pdf_read.py out.pdf --meta` — confirm `page_count`, and per-page `rotation` when you rotated.
- After extraction: check the JSON is non-empty and spot-check a known string or cell.
- Form design loop: `pdf_form_layout.py spec.json` must exit 0; then `--render-overlay boxes.png --pdf form.pdf` and review the PNG with `read` (red = entry boxes with field names, blue = label boxes) asking about overlaps, misalignment, and labels detached from their fields. Iterate spec → lint → overlay until clean.
- After building a form: `pdf_read.py form.pdf --fields` lists every spec field with the right type and options.
- After form fill: `pdf_read.py filled.pdf --fields` and compare values (exact match, including non-ASCII).
- After stamping: re-extract text (pypdf for rotated stamps) or render the page with `pdf_page_image.py` and inspect with `read`.
- After metadata/attachment edits: `pdf_read.py --meta` / `pdf_meta.py --list-attachments`, and re-extract an attachment to byte-compare.
- After encrypt: `--meta` shows `"encrypted": true` and opening without a password fails; after decrypt, text extraction matches the original.
- For anything visual (watermarks, flattened forms), render and inspect with `read`.
