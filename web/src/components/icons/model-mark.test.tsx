import { Box, Waypoints } from "lucide-react"
import { renderToStaticMarkup } from "react-dom/server"
import { expect, it } from "vitest"

import {
  DeepSeekIcon,
  GeminiIcon,
  KimiIcon,
  LongCatIcon,
  MiniMaxIcon,
  QwenIcon,
  XAIIcon,
  XiaomiMiMoIcon,
  ZhipuIcon,
} from "./brand-icons"
import { ModelMark, ProviderMark } from "./model-mark"
import { ClaudeMark, CodexMark, OpenCodeMark } from "./runtime-mark"

it.each([
  ["deepseek-v4-pro", DeepSeekIcon],
  ["glm-5.3", ZhipuIcon],
  ["zhipu/ChatGLM-6B", ZhipuIcon],
  ["kimi-k3", KimiIcon],
  ["minimax-m3", MiniMaxIcon],
  ["qwen3.8-max", QwenIcon],
  ["mimo-v2.6-pro", XiaomiMiMoIcon],
  ["LongCat-Flash", LongCatIcon],
  ["grok-4", XAIIcon],
  ["claude-sonnet-5-5", ClaudeMark],
  ["gemini-3-flash", GeminiIcon],
  ["gpt-6.1-sol", CodexMark],
] as const)("shows the maker's mark for %s", (name, Icon) => {
  expect(renderToStaticMarkup(<ModelMark name={name} className="size-4" />)).toBe(
    renderToStaticMarkup(<Icon className="size-4" />),
  )
})

it("keeps the generic model mark for an unknown maker", () => {
  expect(renderToStaticMarkup(<ModelMark name="unknown-model" />)).toBe(
    renderToStaticMarkup(<Box className="text-muted-foreground" aria-hidden />),
  )
})

it.each(["opencode_go", "opencode_zen"])("reuses the OpenCode runtime mark for %s", (kind) => {
  expect(renderToStaticMarkup(<ProviderMark kind={kind} className="size-6" />)).toBe(
    renderToStaticMarkup(<OpenCodeMark className="size-6" />),
  )
})

it("keeps the generic provider mark for a compatible endpoint", () => {
  expect(renderToStaticMarkup(<ProviderMark kind="openai_compatible" />)).toBe(
    renderToStaticMarkup(<Waypoints className="text-muted-foreground" aria-hidden />),
  )
})
