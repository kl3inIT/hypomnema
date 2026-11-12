import { screen } from "@testing-library/react"
import type { UserEvent } from "@testing-library/user-event"

// choose picks an option of a Select as the owner does: it opens the list from its trigger and clicks the option.
export async function choose(user: UserEvent, trigger: HTMLElement, option: string) {
  await user.click(trigger)
  await user.click(await screen.findByRole("option", { name: option }))
}

// options opens a Select and returns its options' names, then closes it.
export async function options(user: UserEvent, trigger: HTMLElement) {
  await user.click(trigger)
  const names = (await screen.findAllByRole("option")).map((o) => o.textContent)
  await user.keyboard("{Escape}")

  return names
}
