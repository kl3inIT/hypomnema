import { CircleAlert } from "lucide-react"

import { Alert, AlertDescription } from "@/components/ui/alert"
import { errorText } from "@/lib/server-error"

// ErrorAlert says what the server refused, in the server's words.
export function ErrorAlert({ error }: { error: unknown }) {
  if (!error) return null

  return (
    <Alert variant="destructive">
      <CircleAlert />
      <AlertDescription>{errorText(error)}</AlertDescription>
    </Alert>
  )
}
