import { Link } from "@tanstack/react-router"
import {
  ArrowLeft,
  ChevronLeft,
  ChevronRight,
  Download,
  ExternalLink,
  Link2,
  Maximize2,
  Minimize2,
  MoreHorizontal,
  X,
} from "lucide-react"
import { useState, type ReactNode } from "react"

import type { Artifact, ArtifactRevision } from "@/api/generated/types.gen"
import { Button } from "@/components/ui/button"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { toast } from "@/components/ui/toast"
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip"
import { problemOf } from "@/features/files/files-state"
import { since } from "@/lib/time"

import { ArtifactIcon } from "./artifact-icon"
import { downloadURL, pageURL, revisionOf, useArtifact } from "./artifact-state"
import { ArtifactView, type ArtifactMode } from "./artifact-view"

// ArtifactPanel is an artifact opened beside the chat, over the whole screen, or on its own page (issue #402, rows
// 5-7). Its header is AI Elements' Artifact header (Apache-2.0, Copyright 2023 Vercel, Inc.;
// packages/elements/src/artifact.tsx: ArtifactHeader, ArtifactTitle, ArtifactDescription, ArtifactActions,
// ArtifactAction with a tooltip, ArtifactClose), with what Claude's and ChatGPT's artifact panels add: Preview and
// Code for a page (assistant-ui's with-artifacts), the revisions one by one, full screen, a new tab, and a menu
// to download it or copy its link. The revision shown follows the newest until the owner steps back to an older one.
export function ArtifactPanel({
  id,
  rev,
  onRev,
  where,
  onClose,
  onFullScreen,
}: {
  id: string
  // rev is the revision the owner picked; absent, the newest.
  rev?: number
  onRev: (rev: number | undefined) => void
  // where it is shown: beside the chat, over the screen, or on its own page.
  where: "side" | "full" | "page"
  onClose?: () => void
  onFullScreen?: () => void
}) {
  const artifact = useArtifact(id)
  const [mode, setMode] = useState<ArtifactMode>("preview")

  if (artifact.isPending) {
    return (
      <Frame>
        <Header title="Opening…" onClose={onClose} />
      </Frame>
    )
  }

  if (artifact.isError || !artifact.data) {
    return (
      <Frame>
        <Header title="Artifact" onClose={onClose} />
        <output className="flex flex-1 items-center justify-center p-8 text-center text-sm text-muted-foreground">
          {problemOf(artifact.error).message}
        </output>
      </Frame>
    )
  }

  const a = artifact.data
  const revision = revisionOf(a, rev)
  if (!revision) return null

  const revisions = a.revisions ?? []
  const at = revisions.indexOf(revision)
  // Newest first: the one before in time is after in the list.
  const older = revisions[at + 1]
  const newer = revisions[at - 1]
  const step = (to: ArtifactRevision | undefined) =>
    to && onRev(to.revision === a.revision ? undefined : to.revision)

  return (
    <Frame>
      <Header
        title={revision.title}
        type={revision.type}
        description={
          <>
            v{revision.revision}
            {revision.revision !== a.revision && ` of ${a.revision}`} · Updated{" "}
            {since(revision.created_at)}
            {where === "page" && ` · by ${a.bot}`}
          </>
        }
        onClose={onClose}
        start={
          where === "page" && (
            <Action
              label="Back to the chat"
              render={
                <Button
                  variant="ghost"
                  size="icon-sm"
                  aria-label="Back to the chat"
                  nativeButton={false}
                  render={<Link to="/chats/$chatId" params={{ chatId: a.chat_id }} />}
                />
              }
            >
              <ArrowLeft />
            </Action>
          )
        }
      >
        {revision.type === "html" && (
          <fieldset className="m-0 me-1 hidden gap-0.5 rounded-lg border border-solid bg-muted p-0.5 sm:inline-flex">
            <legend className="sr-only">Show</legend>
            {(
              [
                ["preview", "Preview"],
                ["code", "Code"],
              ] as const
            ).map(([value, label]) => (
              <button
                key={value}
                type="button"
                aria-pressed={mode === value}
                onClick={() => setMode(value)}
                className="rounded-md px-2.5 py-0.5 text-xs text-muted-foreground aria-pressed:bg-foreground/9 aria-pressed:text-foreground"
              >
                {label}
              </button>
            ))}
          </fieldset>
        )}
        {revisions.length > 1 && (
          <div className="flex items-center">
            <Action label="Older revision" disabled={!older} onClick={() => step(older)}>
              <ChevronLeft />
            </Action>
            <span className="min-w-10 text-center text-xs text-muted-foreground tabular-nums">
              v{revision.revision}
            </span>
            <Action label="Newer revision" disabled={!newer} onClick={() => step(newer)}>
              <ChevronRight />
            </Action>
          </div>
        )}
        {where !== "page" && onFullScreen && (
          <Action
            label={where === "full" ? "Exit full screen (Esc)" : "Full screen"}
            onClick={onFullScreen}
          >
            {where === "full" ? <Minimize2 /> : <Maximize2 />}
          </Action>
        )}
        {where !== "page" && (
          <Action
            label="Open in a new tab"
            onClick={() => window.open(pageURL(a.id, rev), "_blank", "noopener")}
          >
            <ExternalLink />
          </Action>
        )}
        <More artifact={a} revision={revision} rev={rev} />
      </Header>
      <div className="flex min-h-0 flex-1 flex-col bg-background">
        <ArtifactView
          key={`${revision.revision}-${mode}`}
          artifact={a}
          revision={revision}
          mode={revision.type === "html" ? mode : "preview"}
          wide={where !== "side"}
        />
      </div>
    </Frame>
  )
}

function Frame({ children }: { children: ReactNode }) {
  return <div className="flex min-h-0 min-w-0 flex-1 flex-col">{children}</div>
}

function Header({
  title,
  type,
  description,
  start,
  onClose,
  children,
}: {
  title: string
  type?: ArtifactRevision["type"]
  description?: ReactNode
  start?: ReactNode
  onClose?: () => void
  children?: ReactNode
}) {
  return (
    <div className="flex shrink-0 items-center gap-2 border-b bg-muted/50 py-1.5 ps-3 pe-1.5">
      {start}
      {type && <ArtifactIcon type={type} className="size-4 shrink-0 text-muted-foreground" />}
      <div className="min-w-0 flex-1">
        <p className="truncate text-sm font-medium text-foreground" title={title}>
          {title}
        </p>
        {description && <p className="truncate text-xs text-muted-foreground">{description}</p>}
      </div>
      <div className="flex shrink-0 items-center gap-0.5">
        {children}
        {onClose && (
          <Action label="Close" onClick={onClose}>
            <X />
          </Action>
        )}
      </div>
    </div>
  )
}

// Action is a quiet icon button that says what it does when pointed at (AI Elements' ArtifactAction).
function Action({
  label,
  onClick,
  disabled,
  render,
  children,
}: {
  label: string
  onClick?: () => void
  disabled?: boolean
  render?: React.ReactElement
  children: ReactNode
}) {
  return (
    <Tooltip>
      <TooltipTrigger
        render={
          render ?? (
            <Button
              variant="ghost"
              size="icon-sm"
              aria-label={label}
              disabled={disabled}
              onClick={onClick}
            />
          )
        }
        aria-label={label}
      >
        {children}
      </TooltipTrigger>
      <TooltipContent>{label}</TooltipContent>
    </Tooltip>
  )
}

// More holds what is done with the artifact rather than to the view: download it, copy its link.
function More({
  artifact,
  revision,
  rev,
}: {
  artifact: Artifact
  revision: ArtifactRevision
  rev?: number
}) {
  const folder = revision.files.length > 1

  return (
    <DropdownMenu>
      <DropdownMenuTrigger render={<Button variant="ghost" size="icon-sm" aria-label="More" />}>
        <MoreHorizontal />
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" width="content">
        <DropdownMenuItem
          render={
            // The server sends it as an attachment, so it is saved, never opened as a page of this origin.
            <a
              href={downloadURL(artifact.id, revision)}
              download={folder ? `${artifact.id}.zip` : revision.entry}
              aria-label={folder ? "Download as zip" : `Download ${revision.entry}`}
            />
          }
        >
          <Download />
          {folder ? "Download as zip" : `Download ${revision.entry}`}
        </DropdownMenuItem>
        <DropdownMenuItem
          onClick={() =>
            void navigator.clipboard
              ?.writeText(pageURL(artifact.id, rev))
              .then(() => toast.add({ title: "Link copied", type: "success" }))
              .catch(() => toast.add({ title: "The link could not be copied", type: "error" }))
          }
        >
          <Link2 />
          Copy link
        </DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenu>
  )
}
