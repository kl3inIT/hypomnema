import { Box, Waypoints } from "lucide-react"

import { AntigravityIcon, GeminiIcon } from "@/components/icons/brand-icons"
import { ClaudeMark, CodexMark } from "./runtime-mark"
import { cn } from "@/lib/utils"

// The marks a model picker shows (issue #205). A row leads with its model's logo in assistant-ui's selector
// (MIT License, https://github.com/assistant-ui/assistant-ui; packages/ui/src/components/react/assistant-ui/
// elements/model-selector.tsx, ModelIcon) and in AI Elements' (Apache License 2.0,
// https://github.com/vercel/ai-elements; packages/elements/src/model-selector.tsx, ModelSelectorLogo). AI
// Elements fetches its logos from models.dev; the control plane serves images from itself only, so these are the
// SVG components the project already keeps.

// ModelMark is the mark of a model's maker. The server gives a model's name, provider and kind but no maker, so
// it is read from the name (inferred): AI Elements' example carries the maker in its data.
export function ModelMark({ name, className }: { name: string; className?: string }) {
  const n = name.toLowerCase()

  if (/claude/.test(n)) return <ClaudeMark className={className} />
  if (/gemini/.test(n)) return <GeminiIcon className={className} />
  if (/gpt/.test(n)) return <CodexMark className={className} />

  return <Box className={cn("text-muted-foreground", className)} aria-hidden />
}

// ProviderMark is the mark of what offers a model, by the provider's kind (api/openapi.yaml, Model.kind): a
// subscription has its product's mark, a gateway or a compatible endpoint a plain one.
export function ProviderMark({ kind, className }: { kind: string; className?: string }) {
  if (kind === "claude_subscription" || kind === "anthropic") {
    return <ClaudeMark className={className} />
  }
  if (kind === "codex_subscription") return <CodexMark className={className} />
  if (kind === "antigravity_subscription") return <AntigravityIcon className={className} />

  return <Waypoints className={cn("text-muted-foreground", className)} aria-hidden />
}
