import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { Check, Copy, Link2, Trash2 } from "lucide-react"
import { useId, useState } from "react"

import {
  createArtifactShareMutation,
  listArtifactSharesOptions,
  listArtifactSharesQueryKey,
} from "@/api/generated/@tanstack/react-query.gen"
import { deleteArtifactShare } from "@/api/generated/sdk.gen"
import type { ArtifactShare } from "@/api/generated/types.gen"
import { ConfirmDialog } from "@/components/confirm-dialog"
import { ErrorAlert } from "@/components/error-alert"
import { Button } from "@/components/ui/button"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogPanel,
  DialogTitle,
} from "@/components/ui/dialog"
import { Field, FieldDescription, FieldLabel } from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import { Spinner } from "@/components/ui/spinner"
import { said } from "@/lib/said"
import { since } from "@/lib/time"

// ShareDialog is where the owner makes view-only links to an artifact (issue #745): anyone who has one sees the
// artifact's newest revision at /s/<token> without signing in, until it is revoked. Several links, each named for
// whom it was sent to and revoked on its own (Grafana's per-viewer revoke), listed with their views (Grafana's
// shared dashboards list); Copy, Revoke asked first (the Gateway keys page's ConfirmDialog) and New link.
export function ShareDialog({
  artifactId,
  open,
  onOpenChange,
}: {
  artifactId: string
  open: boolean
  onOpenChange: (open: boolean) => void
}) {
  const options = { path: { artifact_id: artifactId } }
  const shares = useQuery({ ...listArtifactSharesOptions(options), enabled: open })
  const queryClient = useQueryClient()
  const refresh = () =>
    queryClient.invalidateQueries({ queryKey: listArtifactSharesQueryKey(options) })

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Share a view-only link</DialogTitle>
          <DialogDescription>
            Anyone with the link sees this artifact&apos;s newest revision without signing in, until
            you revoke it. Deleting the chat it is in deletes its links.
          </DialogDescription>
        </DialogHeader>
        <DialogPanel>
          <div className="flex flex-col gap-4">
            <NewLink artifactId={artifactId} onMade={refresh} />
            {shares.isPending && (
              <p className="flex items-center gap-2 text-sm text-muted-foreground">
                <Spinner /> Loading…
              </p>
            )}
            <ErrorAlert error={shares.error} />
            {shares.data && shares.data.data.length === 0 && (
              <p className="text-sm text-muted-foreground">No links yet.</p>
            )}
            {shares.data && shares.data.data.length > 0 && (
              <ul aria-label="Links" className="flex flex-col divide-y rounded-xl border">
                {shares.data.data.map((share) => (
                  <ShareRow key={share.id} share={share} onRevoked={refresh} />
                ))}
              </ul>
            )}
          </div>
        </DialogPanel>
      </DialogContent>
    </Dialog>
  )
}

// shareURL is a link as it is sent: this page's own origin and the link's path.
const shareURL = (share: ArtifactShare) => `${window.location.origin}${share.path}`

async function copyLink(share: ArtifactShare) {
  try {
    await navigator.clipboard.writeText(shareURL(share))
    said(`The link for ${share.label} was copied`)
  } catch {
    // No clipboard (an insecure page): the link stays selectable in its field.
  }
}

function NewLink({ artifactId, onMade }: { artifactId: string; onMade: () => Promise<unknown> }) {
  const id = useId()
  const [label, setLabel] = useState("")
  const create = useMutation({
    ...createArtifactShareMutation(),
    onSuccess: async (share) => {
      setLabel("")
      await onMade()
      await copyLink(share)
    },
  })

  return (
    <form
      autoComplete="off"
      className="flex flex-col gap-2"
      onSubmit={(event) => {
        event.preventDefault()
        if (label.trim()) create.mutate({ path: { artifact_id: artifactId }, body: { label } })
      }}
    >
      <Field>
        <FieldLabel htmlFor={id}>For whom</FieldLabel>
        <div className="flex gap-2">
          <Input
            id={id}
            value={label}
            maxLength={80}
            placeholder="Laura"
            onChange={(event) => setLabel(event.target.value)}
          />
          <Button type="submit" disabled={!label.trim() || create.isPending}>
            {create.isPending ? (
              <Spinner data-icon="inline-start" />
            ) : (
              <Link2 data-icon="inline-start" />
            )}
            New link
          </Button>
        </div>
        <FieldDescription>Each link is revoked on its own. A new one is copied.</FieldDescription>
      </Field>
      <ErrorAlert error={create.error} />
    </form>
  )
}

function ShareRow({
  share,
  onRevoked,
}: {
  share: ArtifactShare
  onRevoked: () => Promise<unknown>
}) {
  const [copied, setCopied] = useState(false)

  return (
    <li aria-label={share.label} className="flex items-center gap-2 px-3 py-2">
      <div className="min-w-0 flex-1">
        <p className="truncate text-sm font-medium">{share.label}</p>
        <p className="truncate text-xs text-muted-foreground">
          Made {since(share.created_at)} · {share.views === 1 ? "1 view" : `${share.views} views`}
          {share.last_viewed_at ? ` · last viewed ${since(share.last_viewed_at)}` : ""}
        </p>
      </div>
      <Button
        variant="ghost"
        size="icon-sm"
        aria-label={`Copy the link for ${share.label}`}
        onClick={() =>
          void copyLink(share).then(() => {
            setCopied(true)
          })
        }
      >
        {copied ? <Check /> : <Copy />}
      </Button>
      <ConfirmDialog
        trigger={
          <Button variant="ghost" size="icon-sm" aria-label={`Revoke the link for ${share.label}`}>
            <Trash2 />
          </Button>
        }
        title={`Revoke the link for ${share.label}?`}
        consequence="The link stops working at once, also on a page that is open. This cannot be undone."
        action="Revoke link"
        run={() => deleteArtifactShare({ path: { share_id: share.id } })}
        onDone={onRevoked}
        done={`The link for ${share.label} was revoked`}
      />
    </li>
  )
}
