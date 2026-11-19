import { keepPreviousData, useQuery } from "@tanstack/react-query"
import { Link } from "@tanstack/react-router"
import { KeyRound, TriangleAlert } from "lucide-react"
import { useId, useState } from "react"

import "@/api/client"
import { getGatewayToolConfigOptions } from "@/api/generated/@tanstack/react-query.gen"
import type { GatewayKey, GatewayTool } from "@/api/generated/types.gen"
import { Alert, AlertDescription } from "@/components/ui/alert"
import { buttonVariants } from "@/components/ui/button"
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
import {
  CodeBlock,
  CodeBlockActions,
  CodeBlockContent,
  CodeBlockCopyButton,
  CodeBlockHeader,
  CodeBlockTitle,
} from "@/components/ui/code-block"
import { Empty, EmptyDescription, EmptyHeader, EmptyMedia, EmptyTitle } from "@/components/ui/empty"
import { Field, FieldLabel } from "@/components/ui/field"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { Spinner } from "@/components/ui/spinner"
import { ModelMark } from "@/components/icons/model-mark"
import { RuntimeMark } from "@/components/icons/runtime-mark"
import { ErrorAlert } from "@/components/error-alert"
import { SettingsPage } from "@/components/settings-page"
import { modelName } from "@/lib/model-name"

import { connectKeys, toolNote } from "./connect-state"
import { useGatewayKeys } from "./use-models"

// The tools the page connects, the four the owner named (2026-11-19), each with the mark its CLI has elsewhere
// (runtime-mark.tsx) and a line on what it is, as 9router's CLI tools page lists a card per tool with its logo, name
// and description (src/app/(dashboard)/dashboard/cli-tools/CLIToolsPageClient.js, shared/constants/cliTools.js).
const tools: { id: GatewayTool; runtime: string; name: string; description: string }[] = [
  {
    id: "claude_code",
    runtime: "claude",
    name: "Claude Code",
    description: "Anthropic's coding agent, through the gateway's /v1/messages.",
  },
  {
    id: "codex",
    runtime: "codex",
    name: "Codex",
    description: "OpenAI's Codex CLI, through the gateway's /v1/responses.",
  },
  {
    id: "omp",
    runtime: "omp",
    name: "omp",
    description: "oh-my-pi, on each model's own wire, as the workers run it.",
  },
  {
    id: "opencode",
    runtime: "opencode",
    name: "OpenCode",
    description: "opencode, through the gateway's OpenAI-compatible chat.",
  },
]

// ConnectScreen is Settings → Connect a tool (issue #907): a card for each tool with a key and a model to pick and
// the file that points the tool at the gateway, its path, a copy button and a note on what the file turns on, as
// 9router's CLI tools cards show their "Manual Config" (CodexToolCard.js, ClaudeToolCard.js, OpenCodeToolCard.js
// getManualConfigs; shared/components/ManualConfigModal.js). Only owner keys whose value is stored are offered
// (issue #887), as 9router's ApiKeySelect offers its keys; a key opened from its row on Gateway keys is chosen even
// without one, and its file holds a placeholder. Writing the file onto the machine (9router's "Apply") is not done
// here: the page runs in a browser, away from the machine the tool runs on.
export function ConnectScreen({ keyId }: { keyId?: string }) {
  const keys = useGatewayKeys()
  const offered = connectKeys(keys.data?.data ?? [], keyId)

  return (
    <SettingsPage
      title="Connect a tool"
      description="Point Claude Code, Codex, omp or OpenCode at the gateway: pick a key and a model, then copy the file."
    >
      {keys.isPending && (
        <p className="flex items-center gap-2 text-sm text-muted-foreground">
          <Spinner />
          Loading…
        </p>
      )}
      <ErrorAlert error={keys.error} />
      {keys.data && offered.length === 0 && (
        <Empty>
          <EmptyHeader>
            <EmptyMedia variant="icon">
              <KeyRound />
            </EmptyMedia>
            <EmptyTitle>No key to connect with</EmptyTitle>
            <EmptyDescription>
              Create a gateway key first; a key made now keeps its value, so its file can hold it.
            </EmptyDescription>
          </EmptyHeader>
          <Link to="/settings/gateway-keys" className={buttonVariants({ variant: "outline" })}>
            Gateway keys
          </Link>
        </Empty>
      )}
      {offered.length > 0 &&
        tools.map((tool) => (
          <ToolCard
            key={tool.id}
            tool={tool}
            keys={offered}
            initialKey={offered.some((k) => k.id === keyId) ? keyId! : offered[0]!.id}
          />
        ))}
    </SettingsPage>
  )
}

function ToolCard({
  tool,
  keys,
  initialKey,
}: {
  tool: (typeof tools)[number]
  keys: GatewayKey[]
  initialKey: string
}) {
  const id = useId()
  const [keyId, setKeyId] = useState(initialKey)
  const [model, setModel] = useState<string | undefined>(undefined)
  const config = useQuery({
    ...getGatewayToolConfigOptions({
      path: { key_id: keyId, tool: tool.id },
      query: { base_url: window.location.origin, model },
    }),
    placeholderData: keepPreviousData,
    // The file may hold the key: it is read again when the card is opened again, not kept.
    gcTime: 0,
  })
  const data = config.data
  const note = toolNote(tool.id, data?.model ?? "")

  return (
    <section aria-label={tool.name}>
      <Card>
        <CardHeader>
          <div className="flex items-center gap-2">
            <span className="grid size-7 place-items-center rounded-md border bg-muted">
              <RuntimeMark runtime={tool.runtime} />
            </span>
            <CardTitle>{tool.name}</CardTitle>
          </div>
          <CardDescription>{tool.description}</CardDescription>
        </CardHeader>
        <CardContent>
          <div className="flex flex-col gap-4">
            <div className="grid gap-3 sm:grid-cols-2">
              <Field>
                <FieldLabel htmlFor={`${id}-key`}>Key</FieldLabel>
                <Select
                  value={keyId}
                  onValueChange={(value) => {
                    if (!value) return
                    setKeyId(value)
                    setModel(undefined)
                  }}
                >
                  <SelectTrigger id={`${id}-key`} width="full">
                    <SelectValue>
                      {(value: string) => keys.find((k) => k.id === value)?.name ?? value}
                    </SelectValue>
                  </SelectTrigger>
                  <SelectContent>
                    {keys.map((k) => (
                      <SelectItem key={k.id} value={k.id}>
                        {k.name}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </Field>
              <Field>
                <FieldLabel htmlFor={`${id}-model`}>Model</FieldLabel>
                <Select
                  value={data?.model ?? ""}
                  disabled={!data || data.models.length === 0}
                  onValueChange={(value) => value && setModel(value)}
                >
                  <SelectTrigger id={`${id}-model`} width="full">
                    <SelectValue>
                      {(value: string) =>
                        value ? (
                          <span className="flex min-w-0 items-center gap-2">
                            <ModelMark name={value} className="size-3.5 shrink-0" />
                            <span className="truncate">{modelName(value)}</span>
                          </span>
                        ) : (
                          "No model"
                        )
                      }
                    </SelectValue>
                  </SelectTrigger>
                  <SelectContent>
                    {(data?.models ?? []).map((m) => (
                      <SelectItem key={m} value={m}>
                        <ModelMark name={m} className="size-3.5 shrink-0" />
                        {modelName(m)}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </Field>
            </div>
            <ErrorAlert error={config.error} />
            {data && data.models.length === 0 && (
              <Alert>
                <TriangleAlert />
                <AlertDescription>
                  This key may use no model {tool.name} can call. Give it one on Gateway keys; the
                  file names {"<model>"} until then.
                </AlertDescription>
              </Alert>
            )}
            {data && !data.key_stored && (
              <Alert>
                <TriangleAlert />
                <AlertDescription>
                  This key&apos;s value is not stored, so the file holds {"<your gateway key>"}: put
                  the key in its place, or make a new key, whose value is kept.
                </AlertDescription>
              </Alert>
            )}
            {data?.files.map((file) => (
              <CodeBlock key={file.path} size="sm">
                <CodeBlockHeader>
                  <CodeBlockTitle>{file.path}</CodeBlockTitle>
                  <CodeBlockActions>
                    <CodeBlockCopyButton
                      value={file.content}
                      aria-label={`Copy ${file.path} for ${tool.name}`}
                    />
                  </CodeBlockActions>
                </CodeBlockHeader>
                <CodeBlockContent>{file.content}</CodeBlockContent>
              </CodeBlock>
            ))}
            {note && (
              <p className="text-sm [overflow-wrap:anywhere] text-muted-foreground">{note}</p>
            )}
          </div>
        </CardContent>
      </Card>
    </section>
  )
}
