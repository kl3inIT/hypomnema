// modelName writes a model's id as people read it (issue #208): "claude-opus-5-5" is "Claude Opus 5.5". What is
// stored and sent stays the id.
//
// The rule for an id of a known family is t3code's (MIT License, https://github.com/pingdotgg/t3code;
// packages/shared/src/model.ts, formatModelSlugName and formatCodexModelName): GPT keeps its hyphens with each
// part capitalized, Claude's two version numbers are joined by a point, the parts of a Claude or Gemini id become
// words, a prefix before a slash is kept, and any other id is left as it is. An alias with no version ("opus")
// gets its family's name before it, as OpenMausBot's displayModelLabel puts the provider's name before a label
// with no digit (Apache License 2.0, https://github.com/milind-soni/OpenMausBot; src/lib/model-friendly.ts). It
// gets no version: the server does not say which one an alias stands for (inferred).
export function modelName(id: string) {
  const at = id.lastIndexOf("/") + 1
  const prefix = id.slice(0, at)
  const name = id.slice(at)

  if (/^gpt-\d/i.test(name)) {
    return (
      prefix +
      name.replace(/^gpt/i, "GPT").replace(/-([a-z])/g, (_, c: string) => "-" + c.toUpperCase())
    )
  }

  if (/^(opus|sonnet|haiku|fable)$/i.test(name)) return `${prefix}Claude ${words(name)}`

  if (!/^(claude-(opus|sonnet|haiku|fable)(-\d|$)|gemini-\d)/i.test(name)) return id

  return prefix + words(name.replace(/^(claude-[a-z]+-\d+)-(\d{1,2})(?=-|\[|$)/i, "$1.$2"))
}

function words(name: string) {
  return name
    .split("-")
    .map((part) => part.charAt(0).toUpperCase() + part.slice(1))
    .join(" ")
}

// modelIsId says a model is shown as its id, which modelName leaves as it is: only an id is written in the code
// font, never a name or a sentence such as "Runtime default" (issue #242).
export function modelIsId(id: string | undefined) {
  return id !== undefined && id !== "" && modelName(id) === id
}
