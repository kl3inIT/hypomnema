import { Globe, Mic } from "lucide-react"

import {
  ElevenLabsIcon,
  ExaIcon,
  FirecrawlIcon,
  ParallelIcon,
  TavilyIcon,
  TinyFishIcon,
  YouIcon,
} from "@/components/icons/brand-icons"
import { ModelMark, ProviderMark } from "@/components/icons/model-mark"
import { cn } from "@/lib/utils"

// The marks of the gateway's providers that are not models' makers (issue #723, decision 3.6): one place for the
// Web search page, the Audio page and the Usage page, as LiteLLM shows a provider's logo before a request
// (RequestLogsTableColumns.tsx) and OmniRoute maps its providers to lobe-icons (lobeProviderIcons.ts). The marks
// themselves are brand-icons.tsx's.

// SearchProviderMark is a web search provider's mark, in its colours: Exa's, Tavily's and Firecrawl's as LobeHub's
// colour icons draw them, Parallel's, TinyFish's and You.com's as their websites serve them, the subscriptions' as
// the Models page draws them, and a globe for any other.
export function SearchProviderMark({
  provider,
  className,
}: {
  provider: string
  className?: string
}) {
  switch (provider) {
    case "exa":
      return <ExaIcon className={className} />
    case "tavily":
      return <TavilyIcon className={className} />
    case "firecrawl":
      return <FirecrawlIcon className={className} />
    case "parallel":
      return <ParallelIcon className={cn("text-foreground", className)} />
    case "tinyfish":
      return <TinyFishIcon className={className} />
    case "you":
      return <YouIcon className={className} />
    case "codex":
    case "claude":
    case "antigravity":
      return <ProviderMark kind={`${provider}_subscription`} className={className} />
    default:
      return <Globe className={cn("text-muted-foreground", className)} aria-hidden />
  }
}

// AudioProviderMark is a speech provider's mark: ElevenLabs's as LobeHub draws it; Soniox's, which no reference
// draws, as OmniRoute shows it, a microphone in its colour (src/shared/constants/providers/audio.ts: icon mic,
// #5B5BD6).
export function AudioProviderMark({
  provider,
  className,
}: {
  provider: string
  className?: string
}) {
  if (provider === "elevenlabs") {
    return <ElevenLabsIcon className={cn("text-foreground", className)} />
  }

  return <Mic className={cn("text-[#5B5BD6]", className)} aria-hidden />
}

// RequestMark is the mark a usage row leads with: a web search's provider, a globe for a page read, a speech
// provider's, an image provider's, and otherwise the model's maker.
export function RequestMark({
  model,
  provider,
  className,
}: {
  model: string
  provider: string
  className?: string
}) {
  if (model === "web/search")
    return <SearchProviderMark provider={provider} className={className} />
  if (model.startsWith("web/")) {
    return <Globe className={cn("text-muted-foreground", className)} aria-hidden />
  }
  if (model.startsWith("audio/"))
    return <AudioProviderMark provider={provider} className={className} />
  if (model.startsWith("image/") && provider) {
    return <ProviderMark kind={`${provider}_subscription`} className={className} />
  }

  return <ModelMark name={model} className={className} />
}
