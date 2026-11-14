import { expect, test } from "vitest"

import { modelIsId, modelName } from "./model-name"

// Scenario: the ids the owner's providers list read as names, and an id of no known family is left alone.
test("a model's id reads as its name", () => {
  expect(modelName("claude-opus-5-5")).toBe("Claude Opus 5.5")
  expect(modelName("claude-opus-4-6-thinking")).toBe("Claude Opus 4.6 Thinking")
  expect(modelName("claude-sonnet")).toBe("Claude Sonnet")
  expect(modelName("opus")).toBe("opus")
  expect(modelName("gemini-3.8-flash-high")).toBe("Gemini 3.8 Flash High")
  expect(modelName("gpt-6.1-sol")).toBe("GPT-6.1-Sol")
  expect(modelName("cx/gpt-6.1-sol")).toBe("cx/GPT-6.1-Sol")
  expect(modelName("gpt-oss-120b-medium")).toBe("gpt-oss-120b-medium")
  expect(modelName("qwen-coder")).toBe("qwen-coder")
})

// Scenario: only an id the page shows as it is takes the code font; a name or no model at all does not.
test("only a model shown as its id is an id", () => {
  expect(modelIsId("qwen-coder")).toBe(true)
  expect(modelIsId("claude-opus-5-5")).toBe(false)
  expect(modelIsId(undefined)).toBe(false)
  expect(modelIsId("")).toBe(false)
})
