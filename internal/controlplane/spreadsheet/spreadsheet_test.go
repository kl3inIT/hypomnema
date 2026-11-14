package spreadsheet

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/xuri/excelize/v2"
)

// The cases of MemoryOS's SpreadsheetPreviewTest.java, with the workbooks made by excelize instead of POI.

func workbook(t *testing.T, build func(f *excelize.File)) []byte {
	t.Helper()

	f := excelize.NewFile()
	defer func() { _ = f.Close() }()

	build(f)

	var buf bytes.Buffer
	require.NoError(t, f.Write(&buf))

	return buf.Bytes()
}

func set(t *testing.T, f *excelize.File, sheet, cell string, v any) {
	t.Helper()
	require.NoError(t, f.SetCellValue(sheet, cell, v))
}

func TestEachSheetIsQuotedCSVInWorkbookOrder(t *testing.T) {
	data := workbook(t, func(f *excelize.File) {
		require.NoError(t, f.SetSheetName("Sheet1", "Doanh thu"))
		set(t, f, "Doanh thu", "A1", "Tên")
		set(t, f, "Doanh thu", "B1", "Ghi chú")
		set(t, f, "Doanh thu", "A2", "Hà Nội, VN")
		set(t, f, "Doanh thu", "C2", "nói \"xin chào\"\nlần 2")
		set(t, f, "Doanh thu", "B4", 3)
		_, err := f.NewSheet("Trống")
		require.NoError(t, err)
	})

	sheets, err := readAll(t, data)
	require.NoError(t, err)
	assert.Equal(t, []Sheet{
		{Name: "Doanh thu", CSV: "Tên,Ghi chú\n\"Hà Nội, VN\",,\"nói \"\"xin chào\"\"\nlần 2\"\n\n,3\n"},
		{Name: "Trống", CSV: ""},
	}, sheets)
}

func TestAFormulaShowsTheValueTheWorkbookCachedAndIsNeverEvaluated(t *testing.T) {
	data := workbook(t, func(f *excelize.File) {
		set(t, f, "Sheet1", "A1", 1)
		set(t, f, "Sheet1", "B1", 2)
		// The value cached for A1+B1 is 99, not the 3 a calculation would give: what is shown is what the file holds.
		require.NoError(t, f.SetCellValue("Sheet1", "C1", 99))
		require.NoError(t, f.SetCellFormula("Sheet1", "C1", "A1+B1"))
		// A formula the workbook never calculated has no value to show.
		require.NoError(t, f.SetCellFormula("Sheet1", "D1", "A1*10"))
	})

	sheets, err := readAll(t, data)
	require.NoError(t, err)
	require.Len(t, sheets, 1)
	assert.Equal(t, "1,2,99,\n", sheets[0].CSV)
}

func TestASheetPastTheLimitIsCutAtARowBoundary(t *testing.T) {
	data := workbook(t, func(f *excelize.File) {
		require.NoError(t, f.SetSheetName("Sheet1", "Data"))

		for i := range 10 {
			set(t, f, "Data", fmt.Sprintf("A%d", i+1), fmt.Sprintf("row%d", i))
		}
	})

	cut, err := readWith(t, data, limits{perSheet: 13, total: MaxChars, sheets: MaxSheets, cells: MaxCells})
	require.NoError(t, err)
	assert.Equal(t, []Sheet{{Name: "Data", CSV: "row0\nrow1\n", Truncated: true}}, cut)

	// No whole row fits: an empty preview rather than a broken row.
	none, err := readWith(t, data, limits{perSheet: 3, total: MaxChars, sheets: MaxSheets, cells: MaxCells})
	require.NoError(t, err)
	assert.Empty(t, none[0].CSV)
	assert.True(t, none[0].Truncated)

	whole, err := readAll(t, data)
	require.NoError(t, err)
	assert.False(t, whole[0].Truncated)
}

func TestTheLimitCountsCharactersNotBytes(t *testing.T) {
	data := workbook(t, func(f *excelize.File) {
		set(t, f, "Sheet1", "A1", "Đà Nẵng")
		set(t, f, "Sheet1", "A2", "Huế")
	})

	// "Đà Nẵng\n" is 8 characters and 11 bytes; with "Huế\n" 12 characters.
	sheets, err := readWith(t, data, limits{perSheet: 12, total: MaxChars, sheets: MaxSheets, cells: MaxCells})
	require.NoError(t, err)
	assert.Equal(t, Sheet{Name: "Sheet1", CSV: "Đà Nẵng\nHuế\n"}, sheets[0])
}

func TestAnAbsentRowKeepsItsOwnLine(t *testing.T) {
	data := workbook(t, func(f *excelize.File) {
		set(t, f, "Sheet1", "A1", "header")
		set(t, f, "Sheet1", "A4", "cited")
	})

	sheets, err := readAll(t, data)
	require.NoError(t, err)

	lines := strings.Split(sheets[0].CSV, "\n")
	// Sheet row 3 (counted from 0) is line 3.
	assert.Equal(t, "cited", lines[3])
	assert.Equal(t, []string{"header", "", "", "cited", ""}, lines)
}

func TestEverySheetIsRead(t *testing.T) {
	data := workbook(t, func(f *excelize.File) {
		set(t, f, "Sheet1", "A1", "one")

		for _, name := range []string{"Hai", "Ba"} {
			_, err := f.NewSheet(name)
			require.NoError(t, err)
			set(t, f, name, "B2", strings.ToLower(name))
		}
	})

	sheets, err := readAll(t, data)
	require.NoError(t, err)
	assert.Equal(t, []Sheet{
		{Name: "Sheet1", CSV: "one\n"},
		{Name: "Hai", CSV: "\n,hai\n"},
		{Name: "Ba", CSV: "\n,ba\n"},
	}, sheets)
}

func TestAnythingButAWorkbookIsRefused(t *testing.T) {
	for name, data := range map[string][]byte{
		"csv":   []byte("a,b\n"),
		"empty": nil,
		"zip":   {'P', 'K', 3, 4, 0, 0},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := readAll(t, data)
			require.ErrorIs(t, err, ErrNotWorkbook)
		})
	}
}

// readWith is the sheets read within l; readAll within this server's limits.
func readWith(t *testing.T, data []byte, l limits) ([]Sheet, error) {
	t.Helper()

	wb, err := read(t.Context(), data, l)

	return wb.Sheets, err
}

func readAll(t *testing.T, data []byte) ([]Sheet, error) {
	t.Helper()

	return readWith(t, data, limits{perSheet: MaxCharsPerSheet, total: MaxChars, sheets: MaxSheets, cells: MaxCells})
}

// manySheets is a workbook of n sheets, each the ten rows row0 to row9.
func manySheets(t *testing.T, n int) []byte {
	t.Helper()

	return workbook(t, func(f *excelize.File) {
		for i := range n {
			name := fmt.Sprintf("S%d", i+1)
			if i == 0 {
				require.NoError(t, f.SetSheetName("Sheet1", name))
			} else {
				_, err := f.NewSheet(name)
				require.NoError(t, err)
			}

			for r := range 10 {
				set(t, f, name, fmt.Sprintf("A%d", r+1), fmt.Sprintf("row%d", r))
			}
		}
	})
}

// The whole workbook has a budget as each sheet has (this server's addition to MemoryOS's rule): what one sheet
// takes is not left for the next, which is cut at a row boundary; once nothing is left the later sheets are named,
// empty and cut.
func TestAWorkbookIsCutWhenItsSheetsTogetherPassTheBudget(t *testing.T) {
	data := manySheets(t, 3)

	// Each whole sheet is 50 characters; 60 hold the first and two rows of the second.
	sheets, err := readWith(t, data, limits{perSheet: MaxCharsPerSheet, total: 60, sheets: MaxSheets, cells: MaxCells})
	require.NoError(t, err)
	assert.Equal(t, []Sheet{
		{Name: "S1", CSV: "row0\nrow1\nrow2\nrow3\nrow4\nrow5\nrow6\nrow7\nrow8\nrow9\n"},
		{Name: "S2", CSV: "row0\nrow1\n", Truncated: true},
		{Name: "S3", Truncated: true},
	}, sheets)
}

func TestSheetsPastTheLimitAreLeftOutAndTheWorkbookSaysSo(t *testing.T) {
	data := manySheets(t, 4)

	wb, err := read(t.Context(), data, limits{perSheet: MaxCharsPerSheet, total: MaxChars, sheets: 2, cells: MaxCells})
	require.NoError(t, err)
	assert.True(t, wb.Truncated)
	require.Len(t, wb.Sheets, 2)
	assert.Equal(t, "S2", wb.Sheets[1].Name)

	all, err := Read(t.Context(), data)
	require.NoError(t, err)
	assert.False(t, all.Truncated)
	assert.Len(t, all.Sheets, 4)
}

func TestAReadStopsWhenItsRequestEnds(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := Read(ctx, manySheets(t, 1))
	require.ErrorIs(t, err, context.Canceled)
}

// A row keeps its first cells only; the sheet says it was cut.
func TestARowKeepsItsFirstCellsOnly(t *testing.T) {
	data := workbook(t, func(f *excelize.File) {
		require.NoError(t, f.SetSheetRow("Sheet1", "A1", &[]any{"a", "b", "c", "d"}))
	})

	sheets, err := readWith(t, data, limits{perSheet: MaxCharsPerSheet, total: MaxChars, sheets: MaxSheets, cells: 2})
	require.NoError(t, err)
	assert.Equal(t, []Sheet{{Name: "Sheet1", CSV: "a,b\n", Truncated: true}}, sheets)
}

// A small file whose shared strings unpack past UnzipLimit is refused from what its zip declares, before the part
// is inflated: a zip bomb is not read.
func TestAWorkbookThatUnpacksPastTheLimitIsRefusedAtOnce(t *testing.T) {
	var buf bytes.Buffer

	zw := zip.NewWriter(&buf)
	for _, part := range []struct {
		name string
		size int
	}{
		{"[Content_Types].xml", 0},
		{"xl/workbook.xml", 0},
		{"xl/sharedStrings.xml", UnzipLimit + 1},
	} {
		w, err := zw.Create(part.name)
		require.NoError(t, err)

		_, err = w.Write(bytes.Repeat([]byte("a"), part.size))
		require.NoError(t, err)
	}

	require.NoError(t, zw.Close())
	require.Less(t, buf.Len(), 1<<20, "the file itself is small")

	start := time.Now()
	_, err := Read(t.Context(), buf.Bytes())
	require.ErrorIs(t, err, ErrNotWorkbook)
	assert.Less(t, time.Since(start), 5*time.Second)
}
