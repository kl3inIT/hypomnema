import { Settings2 } from "lucide-react"

import { Button } from "@/components/ui/button"
import {
  DropdownMenu,
  DropdownMenuCheckboxItem,
  DropdownMenuContent,
  DropdownMenuGroup,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"

import { isVisible, type Visibility } from "./column-visibility"

// The View menu of a table, after Beszel's systems-table (MIT, Copyright (c) 2024 henrygd,
// https://github.com/henrygd/beszel; internal/site/src/components/systems-table/systems-table.tsx): an icon button
// that opens a list of the columns to tick, its state in column-visibility.ts. Changed: only the "Visible Fields"
// part of Beszel's menu, as the layout is the page's switch and the tables neither sort nor filter. The Machines and
// Agents tables share it.

// ColumnsMenu lists the columns that can be hidden, ticked when shown. Ticking one leaves the menu open, as
// Beszel's.
export function ColumnsMenu({
  columns,
  visibility,
  onShow,
}: {
  columns: { id: string; name: string }[]
  visibility: Visibility
  onShow: (id: string, visible: boolean) => void
}) {
  return (
    <DropdownMenu>
      <DropdownMenuTrigger render={<Button variant="outline" size="icon" aria-label="View" />}>
        <Settings2 />
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" width="content">
        <DropdownMenuGroup>
          <DropdownMenuLabel>Visible columns</DropdownMenuLabel>
          <DropdownMenuSeparator />
          {columns.map((column) => (
            <DropdownMenuCheckboxItem
              key={column.id}
              checked={isVisible(visibility, column.id)}
              onCheckedChange={(checked) => onShow(column.id, checked)}
            >
              {column.name}
            </DropdownMenuCheckboxItem>
          ))}
        </DropdownMenuGroup>
      </DropdownMenuContent>
    </DropdownMenu>
  )
}
