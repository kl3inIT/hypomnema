import { useLayoutEffect, useState } from "react"

import type { Node } from "@/api/generated/types.gen"
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip"
import { RuntimeMark } from "@/features/agents/runtime-mark"

// RuntimeList is a machine's runtimes on one line: each with its mark and its name while they fit, and the marks
// alone when they do not (owner, 2026-11-10). Pointing at them names each with its version. A row of marks with
// the rest said in a tooltip is how Coder shows a chat's MCP servers
// (site/src/pages/AgentsPage/components/MCPServerIconStack.tsx), Orca a crew's agents
// (components/orchestration-crew/CrewAgentIcons.tsx) and Beszel a system's connections.
export function RuntimeList({ runtimes }: { runtimes: Node["runtimes"] }) {
  // The two elements are kept in state, not in refs: the measuring starts when they exist, whenever the tooltip's
  // trigger gets to render them.
  const [outer, setOuter] = useState<HTMLSpanElement | null>(null)
  const [inner, setInner] = useState<HTMLSpanElement | null>(null)
  const [fits, setFits] = useState(true)

  // The names are measured in a copy that is never shown, so that hiding them does not change the measure. The
  // copy is as wide as its content (w-max): a box placed absolutely is otherwise no wider than its container.
  useLayoutEffect(() => {
    if (!outer || !inner || typeof ResizeObserver === "undefined") return

    const measure = () => setFits(inner.offsetWidth <= outer.clientWidth)
    const watch = new ResizeObserver(measure)
    watch.observe(outer)
    watch.observe(inner)
    measure()
    // The names are wider once the web font replaces the fallback, and that change of an unseen box is not always
    // reported: measure again when the fonts are in.
    void document.fonts?.ready.then(measure)

    return () => watch.disconnect()
  }, [outer, inner])

  const named = (
    <>
      {runtimes.map((r) => (
        <span key={r.runtime} className="inline-flex items-center gap-1.5">
          <RuntimeMark runtime={r.runtime} />
          {r.runtime}
        </span>
      ))}
    </>
  )

  return (
    <Tooltip>
      <TooltipTrigger render={<span className="flex min-w-0 flex-1" />}>
        {/* The box that is measured is this component's own element: the trigger keeps its element's ref. */}
        <span ref={setOuter} className="relative flex min-w-0 flex-1 items-center overflow-hidden">
          <span
            ref={setInner}
            aria-hidden
            className="invisible absolute flex w-max items-center gap-3 whitespace-nowrap"
          >
            {named}
          </span>
          <span className="flex items-center gap-3 whitespace-nowrap">
            {fits
              ? named
              : runtimes.map((r) => (
                  <span key={r.runtime} className="inline-flex items-center">
                    <RuntimeMark runtime={r.runtime} />
                    <span className="sr-only">{r.runtime}</span>
                  </span>
                ))}
          </span>
        </span>
      </TooltipTrigger>
      <TooltipContent>
        <span className="flex flex-col gap-1">
          {runtimes.map((r) => (
            <span key={r.runtime}>{`${r.runtime} ${r.version ?? ""}`.trim()}</span>
          ))}
        </span>
      </TooltipContent>
    </Tooltip>
  )
}
