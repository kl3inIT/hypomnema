import { BookOpen, Globe, Upload } from "lucide-react"

import {
  GitHubIcon,
  GoogleIcon,
  HypomnemaIcon,
  LarkIcon,
  SupersetIcon,
} from "@/components/icons/brand-icons"
import { Badge } from "@/components/ui/badge"
import { cn } from "@/lib/utils"

import { builtinMark } from "./skills-state"

const marks = {
  google: GoogleIcon,
  lark: LarkIcon,
  superset: SupersetIcon,
  hypomnema: HypomnemaIcon,
}

// SkillMark is a skill's icon: a built-in skill that teaches a service's tool wears that service's logo; every
// other skill one book, as Omnara and Coder show every skill with one icon (no picker; owner, 2026-11-12).
export function SkillMark({ name, builtin }: { name: string; builtin?: boolean }) {
  const mark = builtin ? builtinMark(name) : undefined
  const Mark = mark ? marks[mark] : BookOpen

  return (
    <span
      className={cn(
        "flex size-7 shrink-0 items-center justify-center rounded-md border bg-background",
        !mark && "text-muted-foreground",
      )}
      aria-hidden
    >
      <Mark className="size-4" />
    </span>
  )
}

// EveryoneBadge says a skill is given to every worker and bot.
export function EveryoneBadge() {
  return (
    <Badge variant="success">
      <Globe data-icon="inline-start" />
      For everyone
    </Badge>
  )
}

// SourceIcon is where a revision came from: GitHub's mark for a repository, an arrow for an upload.
export function SourceIcon({ source, className }: { source: string; className?: string }) {
  return source.startsWith("github:") ? (
    <GitHubIcon className={className} />
  ) : (
    <Upload className={cn("text-muted-foreground", className)} aria-hidden />
  )
}
