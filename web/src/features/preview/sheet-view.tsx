import { useState } from "react"

import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs"

import { CsvView } from "./csv-view"
import type { Sheets } from "./preview-kind"
import { CutNote } from "./preview-surface"

// SheetView is a workbook, one tab a sheet. Copied from MemoryOS (web/src/features/preview/sheet-view.tsx),
// without its cited rows. The tabs choose the sheet and the sheet is drawn below them, filling what is left, as
// this app's tabs keep their own layout.
export function SheetView({
  sheets,
  truncated = false,
  download,
  filename,
}: {
  sheets: Sheets
  // truncated says the server left out the sheets past its limit (internal/controlplane/spreadsheet, MaxSheets).
  truncated?: boolean
  download: string
  filename: string
}) {
  const [tab, setTab] = useState(0)
  const sheet = sheets[tab]
  if (!sheet)
    return <p className="p-4 text-sm text-muted-foreground">This workbook could not be read.</p>

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <div className="max-w-full shrink-0 overflow-x-auto px-4 pt-3 sm:px-5">
        <Tabs value={String(tab)} onValueChange={(chosen) => setTab(Number(chosen))}>
          <TabsList aria-label="Sheets">
            {sheets.map((s, index) => (
              <TabsTrigger key={index} value={String(index)} title={s.name}>
                <span className="max-w-64 truncate">{s.name}</span>
              </TabsTrigger>
            ))}
          </TabsList>
        </Tabs>
        {truncated && (
          <CutNote download={download} filename={filename} className="mt-2">
            Only the first {sheets.length} sheets are shown.
          </CutNote>
        )}
      </div>
      <CsvView
        key={tab}
        csv={sheet.csv}
        truncated={sheet.truncated}
        download={download}
        filename={filename}
      />
    </div>
  )
}
