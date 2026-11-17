import type {
  AudioKey,
  AudioKind,
  AudioProvider,
  AudioProviderLink,
  AudioUsage,
  WebSearchRest,
} from "@/api/generated/types.gen"

import { type Tone, until } from "./models-state"

// What the Audio page shows of each audio provider (issue #694), worked out from what the server lists. The words
// follow the Web search page's (web-search-state.ts): a provider with a key is "Ready", one with none has "No key",
// one whose every key rests says why and until when, and what was used is this control plane's own count, with the
// seconds heard or the characters spoken, as OmniRoute bills audio (mediaRequestAccounting.ts).

const names: Record<AudioProvider, string> = { soniox: "Soniox", elevenlabs: "ElevenLabs" }

export function audioProviderName(provider: AudioProvider) {
  return names[provider]
}

// Where each provider gives its keys.
export const audioKeyPages: Record<AudioProvider, string> = {
  soniox: "https://console.soniox.com",
  elevenlabs: "https://elevenlabs.io/app/settings/api-keys",
}

const kindNames: Record<AudioKind, string> = { stt: "speech to text", tts: "text to speech" }

export function kindName(kind: AudioKind) {
  return kindNames[kind]
}

export type AudioState = { label: string; tone: Tone; why: string }

const rests: Record<WebSearchRest, { label: string; why: string }> = {
  rate_limit: {
    label: "Rate limited",
    why: "It answered 429 (rate limited) and is skipped until then.",
  },
  no_credits: {
    label: "Out of credits",
    why: "It said its credits are spent. It is asked again after each rest, 5 minutes doubling up to an hour.",
  },
  refused: {
    label: "Refused",
    why: "It refused the key (401 or 403). Check the key, or remove it.",
  },
  daily_limit: {
    label: "Daily limit",
    why: "It reached its daily limit and is asked again at 00:00 UTC.",
  },
}

function restState(rest: WebSearchRest | undefined, restingUntil: string | undefined, now: number) {
  const words = rests[rest ?? "rate_limit"]

  return {
    label: restingUntil ? `${words.label} until ${until(restingUntil, now)}` : words.label,
    tone: (rest === "refused" ? "destructive" : "warning") as Tone,
    why: words.why,
  }
}

// audioState is a provider's badge: what it is now, and why when pointed at.
export function audioState(link: AudioProviderLink, now = Date.now()): AudioState {
  switch (link.state) {
    case "off":
      return {
        label: "Off",
        tone: "outline",
        why: "Switched off: neither speech to text nor text to speech asks it, nor may name it.",
      }
    case "resting":
      return restState(link.rest, link.resting_until, now)
    case "not_set_up":
      return {
        label: "No key",
        tone: "outline",
        why: `${audioProviderName(link.provider)} is asked once one of your keys is in the vault.`,
      }
    case "ready":
      return { label: "Ready", tone: "success", why: "A request asks it in its turn." }
  }
}

// audioKeyState is the badge of one of a provider's keys.
export function audioKeyState(key: AudioKey, now = Date.now()): AudioState {
  if (key.state === "resting") return restState(key.rest, key.resting_until, now)

  return {
    label: "Ready",
    tone: "success",
    why: "It is asked in its turn, the one used longest ago first.",
  }
}

const count = new Intl.NumberFormat()

// heardText is a length of recordings, in the largest unit that reads well.
export function heardText(seconds: number) {
  if (seconds < 60) return `${count.format(Math.round(seconds))} s`
  if (seconds < 3600) return `${count.format(Math.round(seconds / 60))} min`

  return `${count.format(Math.round((seconds / 3600) * 10) / 10)} h`
}

// audioUsageText is this control plane's requests of one kind today and this month (UTC), failures said only when
// there are some, and what the month's requests heard or spoke.
export function audioUsageText(kind: AudioKind, usage: AudioUsage) {
  const part = (label: string, t: AudioUsage["today"]) =>
    `${label} ${count.format(t.answered)}${t.failed > 0 ? ` (+${count.format(t.failed)} failed)` : ""}`
  const amount =
    kind === "stt"
      ? `${heardText(usage.month.seconds)} heard`
      : `${count.format(usage.month.characters)} characters spoken`

  return `${part("Today", usage.today)} · ${part("this month", usage.month)} · ${amount}`
}
