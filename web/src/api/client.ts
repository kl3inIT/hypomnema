import { readToken } from "@/features/auth/token"

import { client } from "./generated/client.gen"

// Every request, the chat stream included, sends the owner's token.
client.setConfig({ auth: () => readToken() ?? undefined })

export { client }
