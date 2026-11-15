// What the server said when it refused a request.

// errorText is the server's message for a refused request (a problem's `message`), or a plain line when it said
// nothing.
export function errorText(error: unknown) {
  if (error && typeof error === "object" && "message" in error && error.message) {
    return String(error.message)
  }

  return "The server refused. Try again."
}

// problemOf is a refused request's code and message, as the server sent them. Unlike errorText it keeps the code,
// and it takes the message only when it is a string.
export function problemOf(error: unknown): { code: string; message: string } {
  if (error && typeof error === "object") {
    const { code, message } = error as Record<string, unknown>

    return {
      code: typeof code === "string" ? code : "",
      message: typeof message === "string" && message ? message : "The server refused. Try again.",
    }
  }

  return { code: "", message: "The server refused. Try again." }
}
