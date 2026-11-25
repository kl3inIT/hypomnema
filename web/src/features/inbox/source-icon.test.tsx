import { render } from "@testing-library/react"
import { expect, test } from "vitest"

import { SourceIcon } from "./source-icon"

// The channel name belongs to the mark's accessible label and tooltip. The surrounding Inbox row or detail names
// the place beside it, so moving the channel name out of that text cannot make the icon unlabeled.
test("labels the source mark with its channel", () => {
  const { container } = render(<SourceIcon source="whatsapp" />)

  expect(container.querySelector('[data-source="whatsapp"]')).toHaveAttribute(
    "aria-label",
    "WhatsApp",
  )
})
