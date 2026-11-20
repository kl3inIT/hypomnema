import { ChevronDown, ChevronRight, MoreHorizontal, Pencil, Trash2 } from "lucide-react"
import { useState } from "react"

import type { Department } from "@/api/generated/types.gen"
import { ConfirmDialog } from "@/components/confirm-dialog"
import { Button } from "@/components/ui/button"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { useDeleteDepartment, useRenameDepartment } from "@/features/bots/use-departments"
import { said } from "@/lib/said"
import { cn } from "@/lib/utils"

import { DepartmentNameDialog } from "./department-name-dialog"
import { RowGrab } from "./reorderable-list"
import type { SortableBindings } from "./use-sortable-bindings"

// What a folded department's header says of its Bots: how many need the owner, else whether one works, as
// OpenMausBot's section mark shows waiting before working (SidebarSectionHeader.tsx sectionMark).
export type DepartmentAttention = { needsYou: number; working: boolean }

// DepartmentHeader is a department's line in the sidebar (issue #471), adapted from OpenMausBot's
// SidebarSectionHeader (Apache License 2.0, https://github.com/milind-soni/OpenMausBot;
// src/components/SidebarSectionHeader.tsx): a chevron and the name that fold the department, the mark of its Bots
// when it is folded, a grabber that moves it among the departments, and a menu to rename or remove it. Removing it
// asks first and sends its Bots back to the Bots in none, as OpenMausBot's deleteSection.
export function DepartmentHeader({
  department,
  collapsed,
  attention,
  onToggle,
  sortable,
}: {
  department: Department
  collapsed: boolean
  attention: DepartmentAttention
  onToggle: () => void
  sortable?: SortableBindings
}) {
  const [renaming, setRenaming] = useState(false)
  const [removing, setRemoving] = useState(false)
  const rename = useRenameDepartment()
  const remove = useDeleteDepartment()
  const Chevron = collapsed ? ChevronRight : ChevronDown
  const { name } = department

  return (
    <div className="group/department relative flex items-center gap-1 px-1 pt-3 pb-1">
      {sortable && !sortable.disabled && (
        <RowGrab
          ariaLabel={`Move department ${name}`}
          dragging={sortable.dragging}
          dragHandleProps={sortable.dragHandleProps}
          className="size-5 shrink-0"
        >
          <span className="size-4" />
        </RowGrab>
      )}
      <button
        type="button"
        onClick={onToggle}
        aria-expanded={!collapsed}
        aria-label={`${collapsed ? "Expand" : "Collapse"} ${name}`}
        className="flex min-w-0 flex-1 items-center gap-1 rounded-md px-1 py-0.5 text-left outline-none focus-visible:ring-2 focus-visible:ring-ring"
      >
        <Chevron className="size-3.5 shrink-0 text-muted-foreground" aria-hidden />
        <span className="truncate text-xs font-medium text-muted-foreground">{name}</span>
        {collapsed && <Mark attention={attention} />}
      </button>
      <DropdownMenu>
        <span className="flex transition-opacity has-[[data-popup-open]]:opacity-100 md:opacity-0 md:group-focus-within/department:opacity-100 md:group-hover/department:opacity-100">
          <DropdownMenuTrigger
            render={
              <Button
                variant="ghost"
                size="icon-xs"
                aria-label={`Actions for department ${name}`}
              />
            }
          >
            <MoreHorizontal />
          </DropdownMenuTrigger>
        </span>
        <DropdownMenuContent align="end" width="content">
          <DropdownMenuItem onClick={() => setRenaming(true)}>
            <Pencil /> Rename…
          </DropdownMenuItem>
          <DropdownMenuSeparator />
          <DropdownMenuItem variant="destructive" onClick={() => setRemoving(true)}>
            <Trash2 /> Delete department
          </DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>
      {renaming && (
        <DepartmentNameDialog
          title="Rename department"
          action="Rename"
          initial={name}
          pending={rename.isPending}
          error={rename.error}
          onSave={(next) =>
            rename.mutate(
              { id: department.id, name: next },
              {
                onSuccess: () => {
                  setRenaming(false)
                  said("Department renamed")
                },
              },
            )
          }
          onClose={() => {
            setRenaming(false)
            rename.reset()
          }}
        />
      )}
      <ConfirmDialog
        open={removing}
        onOpenChange={setRemoving}
        title={`Delete “${name}”?`}
        consequence="Its Bots go back to the list. Their chats, memory and settings stay."
        action="Delete"
        run={() => remove.mutateAsync(department.id)}
        onDone={() => undefined}
        done="Department deleted"
      />
    </div>
  )
}

// Mark is a folded department's sign that one of its Bots needs the owner, with how many, or that one works.
function Mark({ attention }: { attention: DepartmentAttention }) {
  if (attention.needsYou > 0) {
    return (
      <span
        data-testid="department-needs-you"
        className="ms-1 flex h-4 min-w-4 items-center justify-center rounded-full bg-needs-you px-1 text-[10px] leading-none font-bold text-sidebar"
      >
        {attention.needsYou}
        <span className="sr-only"> need you</span>
      </span>
    )
  }

  return attention.working ? (
    <span className={cn("ms-1 size-2 shrink-0 rounded-full bg-running")}>
      <span className="sr-only">Working</span>
    </span>
  ) : null
}
