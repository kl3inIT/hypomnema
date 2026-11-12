import { ChevronRight, File, Folder } from "lucide-react"
import { useMemo, useState } from "react"

import type { SkillFile } from "@/api/generated/types.gen"
import { cn } from "@/lib/utils"

import { skillFileTree, type SkillFileTreeNode } from "./file-tree"

// The files of a skill's revision as a folder tree, copied from Omnara (Apache-2.0, Copyright Omnara,
// https://github.com/omnara-ai/omnara; frontend/apps/web/src/components/skills/SkillFileTree.tsx and
// src/lib/skill-file-tree.ts). Changed: lucide's icons.

export function SkillFileTree({ files, className }: { files: SkillFile[]; className?: string }) {
  const tree = useMemo(() => skillFileTree(files.map((file) => file.path)), [files])

  return (
    <ul
      aria-label="Skill files"
      className={cn("min-w-0 overflow-y-auto font-mono text-xs leading-6", className)}
    >
      {tree.map((node) => (
        <SkillFileTreeItem key={node.path} node={node} />
      ))}
    </ul>
  )
}

function SkillFileTreeItem({ node }: { node: SkillFileTreeNode }) {
  const [open, setOpen] = useState(true)
  if (!node.children) {
    return (
      <li className="flex min-w-0 items-center gap-1.5 pl-5" title={node.path}>
        <File aria-hidden="true" className="size-3.5 shrink-0 text-muted-foreground" />
        <span className="truncate">{node.name}</span>
      </li>
    )
  }

  return (
    <li>
      <button
        type="button"
        aria-expanded={open}
        title={node.path}
        className="flex w-full min-w-0 items-center gap-1.5 rounded-sm text-left outline-none hover:bg-muted focus-visible:ring-2 focus-visible:ring-ring/50"
        onClick={() => {
          setOpen((current) => !current)
        }}
      >
        <ChevronRight
          aria-hidden="true"
          className={cn("size-3.5 shrink-0 transition-transform", open && "rotate-90")}
        />
        <Folder aria-hidden="true" className="size-3.5 shrink-0 text-muted-foreground" />
        <span className="truncate">{node.name}</span>
      </button>
      {open && (
        <ul className="ml-[0.4375rem] border-l pl-2">
          {node.children.map((child) => (
            <SkillFileTreeItem key={child.path} node={child} />
          ))}
        </ul>
      )}
    </li>
  )
}
