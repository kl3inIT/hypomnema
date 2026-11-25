// The project filter is adapted from circle (https://github.com/ln-dev7/circle, commit b0ba7dd):
// components/layout/headers/projects/filter.tsx, a button opening a Command list with pages, each page with a back
// button and its own list; adapted to the app's Popover, Command and Button (Base UI) and to the Inbox's projects
// (issue #1157), which the owner also adds, renames and removes from its pages.
//
// MIT License
//
// Copyright (c) 2025 lndev-ui | Circle Template
//
// Permission is hereby granted, free of charge, to any person obtaining a copy of this software and associated
// documentation files (the "Software"), to deal in the Software without restriction, including without limitation the
// rights to use, copy, modify, merge, publish, distribute, sublicense, and/or sell copies of the Software, and to
// permit persons to whom the Software is furnished to do so, subject to the following conditions:
//
// The above copyright notice and this permission notice shall be included in all copies or substantial portions of
// the Software.
//
// THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR IMPLIED, INCLUDING BUT NOT LIMITED TO
// THE WARRANTIES OF MERCHANTABILITY, FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
// AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER LIABILITY, WHETHER IN AN ACTION OF CONTRACT,
// TORT OR OTHERWISE, ARISING FROM, OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
// SOFTWARE.

import { Box, ChevronLeft, ChevronRight, FolderIcon, Pencil, Plus, Trash2 } from "lucide-react"
import { useState } from "react"

import type { InboxProject } from "@/api/generated/types.gen"
import { Button } from "@/components/ui/button"
import {
  Command,
  CommandEmpty,
  CommandGroup,
  CommandInput,
  CommandItem,
  CommandList,
  CommandSeparator,
} from "@/components/ui/command"
import { Input } from "@/components/ui/input"
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover"
import { ProjectDot } from "@/components/ui/project-selector"
import { toast } from "@/components/ui/toast"
import { errorText } from "@/lib/server-error"

import { noProject } from "./inbox-state"
import { useProjectWrite } from "./use-inbox"

// projectColors are the colours a project is given, those of circle's mock data (mock-data/projects.ts, health) and
// the indigo, teal, orange and purple of Linear's palette.
const projectColors = [
  "#5e6ad2",
  "#26b5ce",
  "#4cb782",
  "#f2c94c",
  "#f2994a",
  "#eb5757",
  "#bb87fc",
  "#8f9299",
]

type Page = { kind: "root" } | { kind: "new" } | { kind: "edit"; project: InboxProject }

// ProjectFilter picks the project the Inbox shows, "Every project" by default, as Linear's project filter; its pages
// add a project, and rename, recolour or remove one. counts is how many rows each project has, by its id, and
// noProject's.
export function ProjectFilter({
  projects,
  counts,
  value,
  onChange,
}: {
  projects: InboxProject[]
  counts: Record<string, number>
  value: string | null
  onChange: (project: string | null) => void
}) {
  const [open, setOpen] = useState(false)
  const [page, setPage] = useState<Page>({ kind: "root" })
  const current = value && value !== noProject ? projects.find((p) => p.id === value) : undefined
  const label = current?.name ?? (value === noProject ? "No project" : "Every project")
  const pick = (project: string | null) => {
    onChange(project)
    setOpen(false)
  }

  return (
    <Popover
      open={open}
      onOpenChange={(next) => {
        setOpen(next)
        if (!next) setPage({ kind: "root" })
      }}
    >
      <PopoverTrigger
        render={<Button variant="outline" aria-label={`Project: ${label}`} title={label} />}
      >
        {current ? <ProjectDot color={current.color} /> : <Box />}
        <span className="hidden max-w-28 truncate sm:inline">{label}</span>
      </PopoverTrigger>
      <PopoverContent align="end">
        {page.kind === "root" ? (
          <Command>
            <CommandInput placeholder="Filter by project..." />
            <CommandList>
              <CommandEmpty>No projects found.</CommandEmpty>
              <CommandGroup>
                <CommandItem
                  value="Every project"
                  data-checked={value === null}
                  onSelect={() => pick(null)}
                >
                  <Box className="text-muted-foreground" />
                  Every project
                </CommandItem>
                <CommandItem
                  value="No project"
                  data-checked={value === noProject}
                  onSelect={() => pick(noProject)}
                >
                  <FolderIcon className="text-muted-foreground" />
                  <span className="flex-1">No project</span>
                  <Count n={counts[noProject]} />
                </CommandItem>
                {projects.map((p) => (
                  <CommandItem
                    key={p.id}
                    value={`${p.name} ${p.id}`}
                    data-checked={value === p.id}
                    onSelect={() => pick(p.id)}
                  >
                    <ProjectDot color={p.color} />
                    <span className="min-w-0 flex-1 truncate">{p.name}</span>
                    <Count n={counts[p.id]} />
                  </CommandItem>
                ))}
              </CommandGroup>
              <CommandSeparator />
              <CommandGroup heading="Projects">
                <CommandItem value="New project" onSelect={() => setPage({ kind: "new" })}>
                  <Plus className="text-muted-foreground" />
                  New project…
                </CommandItem>
                {projects.map((p) => (
                  <CommandItem
                    key={p.id}
                    value={`Edit ${p.name} ${p.id}`}
                    onSelect={() => setPage({ kind: "edit", project: p })}
                  >
                    <Pencil className="text-muted-foreground" />
                    <span className="min-w-0 truncate">Edit {p.name}</span>
                    <ChevronRight className="ms-auto" />
                  </CommandItem>
                ))}
              </CommandGroup>
            </CommandList>
          </Command>
        ) : (
          <ProjectForm
            key={page.kind === "edit" ? page.project.id : "new"}
            project={page.kind === "edit" ? page.project : undefined}
            onBack={() => setPage({ kind: "root" })}
            onRemoved={(id) => {
              if (value === id) onChange(null)
            }}
          />
        )}
      </PopoverContent>
    </Popover>
  )
}

function Count({ n }: { n?: number }) {
  if (!n) return null

  return <span className="text-xs text-muted-foreground tabular-nums">{n}</span>
}

// ProjectForm is a page of the filter that adds a project or changes one: its name, its colour, and a way to remove
// it, after which its items have none.
function ProjectForm({
  project,
  onBack,
  onRemoved,
}: {
  project?: InboxProject
  onBack: () => void
  onRemoved: (id: string) => void
}) {
  const write = useProjectWrite()
  const [name, setName] = useState(project?.name ?? "")
  const [color, setColor] = useState(project?.color ?? projectColors[0]!)
  const fail = (title: string) => (error: unknown) =>
    toast.add({ title, description: errorText(error), type: "error" })
  const save = () => {
    const trimmed = name.trim()
    if (!trimmed) return
    write.mutate(
      project
        ? { act: "edit", id: project.id, name: trimmed, color }
        : { act: "create", name: trimmed, color },
      {
        onSuccess: () => {
          toast.add({
            title: project ? "Project saved" : `Project ${trimmed} added`,
            type: "success",
          })
          onBack()
        },
        onError: fail("Not saved"),
      },
    )
  }

  return (
    <div className="flex flex-col gap-3 p-2">
      <div className="flex items-center gap-2">
        <Button variant="ghost" size="icon-xs" aria-label="Back" onClick={onBack}>
          <ChevronLeft />
        </Button>
        <span className="text-sm font-medium">{project ? "Edit project" : "New project"}</span>
      </div>
      <form
        className="flex flex-col gap-3"
        onSubmit={(e) => {
          e.preventDefault()
          save()
        }}
      >
        <Input
          aria-label="Project name"
          placeholder="Name, such as VMAP"
          value={name}
          maxLength={60}
          onChange={(e) => setName(e.target.value)}
        />
        <fieldset aria-label="Colour" className="flex flex-wrap gap-1.5">
          {projectColors.map((c) => (
            <button
              key={c}
              type="button"
              aria-pressed={c === color}
              aria-label={c}
              onClick={() => setColor(c)}
              className="flex size-6 items-center justify-center rounded-full ring-offset-2 ring-offset-popover outline-none focus-visible:ring-2 focus-visible:ring-ring aria-pressed:ring-2 aria-pressed:ring-foreground/60"
            >
              <ProjectDot color={c} size="lg" />
            </button>
          ))}
        </fieldset>
        <div className="flex items-center gap-2">
          <Button type="submit" size="sm" disabled={!name.trim() || write.isPending}>
            {project ? "Save" : "Add project"}
          </Button>
          {project && (
            <Button
              type="button"
              size="sm"
              variant="ghost"
              disabled={write.isPending}
              onClick={() =>
                write.mutate(
                  { act: "delete", id: project.id },
                  {
                    onSuccess: () => {
                      toast.add({ title: `Project ${project.name} removed`, type: "success" })
                      onRemoved(project.id)
                      onBack()
                    },
                    onError: fail("Not removed"),
                  },
                )
              }
            >
              <Trash2 data-icon="inline-start" />
              Remove
            </Button>
          )}
        </div>
      </form>
    </div>
  )
}
