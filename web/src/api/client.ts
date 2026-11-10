// Every request, the chat stream included, carries the session's cookie: the browser sends it by itself to the
// page's own origin. Loading the session here makes a refused request sign the page out.
import "@/features/auth/session"

import { client } from "./generated/client.gen"

export { client }
