import { ArrowDown } from "lucide-react"

export function ScrollToBottomButton({ label, onClick }: { label: string; onClick: () => void }) {
  return (
    <button
      type="button"
      aria-label={label}
      className="absolute bottom-3 left-1/2 flex size-8 -translate-x-1/2 items-center justify-center rounded-full border bg-card text-muted-foreground shadow-xs hover:text-foreground"
      onClick={onClick}
    >
      <ArrowDown className="size-4" />
    </button>
  )
}
