// Package spreadsheet reads a workbook into CSV text a sheet, for the web client's preview of a file a Bot hands
// over or one in a chat's folder (issue #422). The browser ships no workbook parser: the control plane reads the
// workbook and sends each sheet as CSV.
//
// It follows MemoryOS's SpreadsheetPreview.java (memoryos-core/src/main/java/io/memoryos/document/, the owner's own
// code), itself after Onyx's parse_spreadsheet_for_preview (backend/onyx/server/query_and_chat/chat_utils.py): the
// rows are read as a stream, a formula shows the value the workbook cached and is never evaluated, a sheet is cut at
// a row boundary once its text would pass MaxCharsPerSheet, and, unlike Onyx, a row the sheet never stored keeps
// its own empty line. The reader is excelize's row iterator (BSD-3-Clause), in place of Apache POI's
// XSSFSheetXMLHandler.
package spreadsheet

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/xuri/excelize/v2"
)

// MaxCharsPerSheet is the text a sheet's preview holds at most: Onyx's MAX_PREVIEW_CHARS_PER_SHEET.
const MaxCharsPerSheet = 500_000

// MaxChars and MaxSheets bound a whole workbook, which MemoryOS and Onyx leave unbounded (a workbook of many
// sheets would be read whole): this server's own limits, added after the #422 security review. A sheet read past
// what is left of MaxChars is cut as one past MaxCharsPerSheet is; once nothing is left, the later sheets are
// listed by name, empty and cut; sheets past MaxSheets are left out, and Workbook.Truncated says so.
const (
	MaxChars  = 2_000_000
	MaxSheets = 50
)

// What a read may take, this server's own bounds after the security review (MemoryOS and Onyx have none):
//   - UnzipLimit is what a workbook's parts may inflate to, all together, by the sizes its zip declares, which
//     archive/zip holds each part to: a workbook is at most 10 MiB (what /raw reads, an Artifact's 5 MB), and
//     one that unpacks past 64 MiB is refused before a part is read (excelize's own default is 16 GB). Every part
//     is kept in memory (excelize's XML limit is set to the same), so nothing is written to a temporary file.
//   - MaxCells is the cells of a row kept; the rest are left out and the sheet marked cut.
//   - Reads is how many workbooks are read at once; a request waits for its turn while its context lasts, and one
//     whose context ends stops waiting for its read, which then stops at its next row.
const (
	UnzipLimit = 64 << 20
	MaxCells   = 1024
	Reads      = 2
)

var turns = make(chan struct{}, Reads)

// ErrNotWorkbook is a file that is not an xlsx workbook, or one that cannot be read.
var ErrNotWorkbook = errors.New("not a readable xlsx workbook")

// Sheet is one sheet of a workbook as CSV text; Truncated says rows were left out past a limit.
type Sheet struct {
	Name      string
	CSV       string
	Truncated bool
}

// Workbook is a workbook's sheets in its order; Truncated says sheets past MaxSheets were left out.
type Workbook struct {
	Sheets    []Sheet
	Truncated bool
}

// limits are what one read may hold.
type limits struct{ perSheet, total, sheets, cells int }

// Read reads the sheets of an xlsx workbook, in the workbook's order, each cut at MaxCharsPerSheet and all at
// MaxChars, at most MaxSheets of them and MaxCells cells a row. It returns ctx's error as soon as ctx ends, whether
// it was waiting for its turn or reading.
func Read(ctx context.Context, data []byte) (Workbook, error) {
	select {
	case turns <- struct{}{}:
	case <-ctx.Done():
		return Workbook{}, ctx.Err()
	}

	type result struct {
		wb  Workbook
		err error
	}

	done := make(chan result, 1)

	go func() {
		defer func() { <-turns }()

		wb, err := read(ctx, data,
			limits{perSheet: MaxCharsPerSheet, total: MaxChars, sheets: MaxSheets, cells: MaxCells})
		done <- result{wb, err}
	}()

	select {
	case r := <-done:
		return r.wb, r.err
	case <-ctx.Done():
		return Workbook{}, ctx.Err()
	}
}

func read(ctx context.Context, data []byte, l limits) (Workbook, error) {
	f, err := excelize.OpenReader(bytes.NewReader(data),
		excelize.Options{UnzipSizeLimit: UnzipLimit, UnzipXMLSizeLimit: UnzipLimit})
	if err != nil {
		return Workbook{}, fmt.Errorf("%w: %w", ErrNotWorkbook, err)
	}
	defer func() { _ = f.Close() }()

	names := f.GetSheetList()
	if len(names) == 0 {
		return Workbook{}, fmt.Errorf("%w: it has no sheets", ErrNotWorkbook)
	}

	out := Workbook{Truncated: len(names) > l.sheets}
	names = names[:min(len(names), l.sheets)]
	out.Sheets = make([]Sheet, 0, len(names))
	left := l.total

	for _, name := range names {
		if left <= 0 {
			out.Sheets = append(out.Sheets, Sheet{Name: name, Truncated: true})

			continue
		}

		s, n, err := readSheet(ctx, f, name, min(l.perSheet, left), l.cells)
		if ctx.Err() != nil {
			return Workbook{}, ctx.Err()
		}

		if err != nil {
			return Workbook{}, fmt.Errorf("%w: sheet %q: %w", ErrNotWorkbook, name, err)
		}

		out.Sheets = append(out.Sheets, s)
		left -= n
	}

	return out, nil
}

// readSheet is one sheet's rows as CSV. The iterator gives every row up to the last stored, a row the sheet never
// stored as no cells, so each sheet row is its own line, as MemoryOS keeps it.
func readSheet(ctx context.Context, f *excelize.File, name string, maxChars, maxCells int) (Sheet, int, error) {
	rows, err := f.Rows(name)
	if err != nil {
		return Sheet{}, 0, err
	}
	defer func() { _ = rows.Close() }()

	var (
		text  strings.Builder
		chars int
		line  strings.Builder
		// gap is the empty lines of the rows without a value before the next row with one: they go with it, or not
		// at all when the sheet ends, as MemoryOS writes an absent row's line only before a row it reads.
		gap int
		// cut says a row lost its cells past maxCells.
		cut bool
	)

	for rows.Next() {
		if err := ctx.Err(); err != nil {
			return Sheet{}, 0, err
		}

		cells, err := rows.Columns()
		if err != nil {
			return Sheet{}, 0, err
		}

		if len(cells) == 0 {
			gap++

			continue
		}

		if len(cells) > maxCells {
			cells, cut = cells[:maxCells], true
		}

		line.Reset()
		line.WriteString(strings.Repeat("\n", gap))

		for i, c := range cells {
			if i > 0 {
				line.WriteByte(',')
			}

			line.WriteString(quote(c))
		}

		line.WriteByte('\n')

		n := utf8.RuneCountInString(line.String())
		if chars+n > maxChars {
			return Sheet{Name: name, CSV: text.String(), Truncated: true}, chars, nil
		}

		text.WriteString(line.String())
		chars += n
		gap = 0
	}

	if err := rows.Error(); err != nil {
		return Sheet{}, 0, err
	}

	return Sheet{Name: name, CSV: text.String(), Truncated: cut}, chars, nil
}

// quote is a cell as a CSV field (RFC 4180): quoted, with its quotes doubled, when it holds a comma, a quote or a
// line break.
func quote(v string) string {
	if !strings.ContainsAny(v, ",\"\n\r") {
		return v
	}

	return `"` + strings.ReplaceAll(v, `"`, `""`) + `"`
}
