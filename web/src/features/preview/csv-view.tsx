import { useVirtualizer } from "@tanstack/react-virtual"
import { cn } from "cn"
import { memo, useMemo, useRef } from "react"

import { MAX_TABLE_ROWS, parseCsv } from "./preview-kind"
import { CutNote } from "./preview-surface"

// Copied from MemoryOS (web/src/features/preview/csv-view.tsx), without its cited rows, drawn with the classes of
// this app's table (components/ui/table.tsx): its container scrolls both ways, which a virtualized table needs from
// one element.

// Every cell is one truncated line, so each row is the same height and none has to be measured.
const ROW_HEIGHT = 33

// Rows kept rendered beyond the visible ones, so a flick of the wheel never shows empty space.
const OVERSCAN = 16

// Column widths are pinned before rendering, because a virtualized table cannot size columns to content.
const CHARACTER = 8
const CELL_PADDING = 32
const MIN_COLUMN = 72
const MAX_COLUMN = 320

// How many rows the widths are judged from; a sheet is regular enough that a sample settles them.
const WIDTH_SAMPLE = 200

// CsvView is a sheet as a table: the first row is the header, the first column stays visible while wide tables
// scroll sideways, and only the rows in view are rendered. A sheet past MAX_TABLE_ROWS, or one the server or the
// reader cut, says so beside the download.
export function CsvView({
  csv,
  truncated = false,
  download,
  filename,
}: {
  csv: string
  truncated?: boolean
  download: string
  filename: string
}) {
  const scroller = useRef<HTMLDivElement>(null)
  const sheet = useMemo(() => parseSheet(csv), [csv])
  // TanStack Virtual hands back functions the React Compiler cannot memoize; the table is drawn afresh as it is
  // (files-panel.tsx).
  // oxlint-disable-next-line react/incompatible-library
  const rows = useVirtualizer({
    count: sheet.rows.length,
    getScrollElement: () => scroller.current,
    estimateSize: () => ROW_HEIGHT,
    overscan: OVERSCAN,
  })

  if (!sheet.widths.length)
    return <p className="p-4 text-sm text-muted-foreground">An empty sheet</p>

  const shown = rows.getVirtualItems()
  const before = shown[0]?.start ?? 0
  const after = rows.getTotalSize() - (shown[shown.length - 1]?.end ?? 0)
  const cut = truncated || sheet.cut

  return (
    <div className="flex min-h-0 flex-1 flex-col gap-2 p-4 sm:px-5">
      <div className="flex min-h-0 flex-col overflow-hidden rounded-lg border bg-background">
        <div ref={scroller} data-slot="table-container" className="min-h-0 overflow-auto">
          <table
            data-slot="table"
            className="table-fixed caption-bottom text-sm"
            style={{ width: sheet.width }}
          >
            <colgroup>
              {sheet.widths.map((width, index) => (
                <col key={index} style={{ width }} />
              ))}
            </colgroup>
            <thead data-slot="table-header" className="sticky top-0 z-20 [&_tr]:border-b">
              <tr data-slot="table-row" className="border-b">
                {sheet.widths.map((_, index) => (
                  <th
                    key={index}
                    data-slot="table-head"
                    className={cn(
                      "h-8 truncate bg-muted px-3 text-left align-middle text-xs font-medium whitespace-nowrap text-foreground",
                      index === 0 && "sticky left-0 z-10 border-r",
                    )}
                  >
                    {sheet.header[index] ?? ""}
                  </th>
                ))}
              </tr>
            </thead>
            <tbody data-slot="table-body" className="[&_tr:last-child]:border-0">
              <Spacer height={before} columns={sheet.widths.length} />
              {shown.map((item) => (
                <SheetRow
                  key={item.index}
                  row={sheet.rows[item.index] ?? []}
                  columns={sheet.widths.length}
                />
              ))}
              <Spacer height={after} columns={sheet.widths.length} />
            </tbody>
          </table>
        </div>
      </div>
      {cut && (
        <CutNote download={download} filename={filename}>
          {sheet.cut
            ? `Only the first ${MAX_TABLE_ROWS.toLocaleString("en")} rows are shown.`
            : "Only the beginning of this sheet is shown."}
        </CutNote>
      )}
    </div>
  )
}

// Spacer is the rows outside the window, as height rather than elements, so the scrollbar spans the whole sheet.
function Spacer({ height, columns }: { height: number; columns: number }) {
  if (height <= 0) return null

  return (
    <tr aria-hidden="true">
      <td aria-hidden="true" colSpan={columns} style={{ height, padding: 0, border: 0 }} />
    </tr>
  )
}

// SheetRow is one drawn row; its props are plain values, so a scroll leaves the rows still in view untouched.
const SheetRow = memo(function SheetRow({
  row,
  columns,
}: {
  row: readonly string[]
  columns: number
}) {
  return (
    <tr
      data-slot="table-row"
      style={{ height: ROW_HEIGHT }}
      className="border-b transition-colors hover:bg-muted/50"
    >
      {Array.from({ length: columns }, (_, index) => (
        <td
          key={index}
          data-slot="table-cell"
          title={row[index] || undefined}
          className={cn(
            "truncate px-3 align-middle whitespace-nowrap",
            index === 0 && "sticky left-0 border-r bg-background",
          )}
        >
          {row[index] ?? ""}
        </td>
      ))}
    </tr>
  )
})

// parseSheet is the parsed sheet and the column widths it is drawn with, worked out once a sheet.
function parseSheet(csv: string) {
  const [header = [], ...body] = parseCsv(csv)
  const rows = body.slice(0, MAX_TABLE_ROWS)
  const columns = Math.max(header.length, ...rows.map((row) => row.length))
  const widths: number[] = []
  for (let column = 0; column < columns; column++) {
    let longest = header[column]?.length ?? 0
    for (const row of rows.slice(0, WIDTH_SAMPLE)) {
      longest = Math.max(longest, row[column]?.length ?? 0)
    }
    widths.push(Math.min(Math.max(longest * CHARACTER + CELL_PADDING, MIN_COLUMN), MAX_COLUMN))
  }

  return {
    header,
    rows,
    widths,
    width: widths.reduce((total, width) => total + width, 0),
    cut: body.length > MAX_TABLE_ROWS,
  }
}
