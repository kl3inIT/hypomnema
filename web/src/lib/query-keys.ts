// everyCall matches every cached call of one generated operation, whatever its path, query or base URL: a
// generated query key is one object named by the operation's _id, and TanStack Query matches a key by its
// prefix, so the _id alone matches them all (MemoryOS web/src/features/chat/chat-api.ts, everyCall). An exact
// generated key built with `query: {}` does not match one built without `query`; this one matches both.
export function everyCall([{ _id }]: readonly [{ _id: string }]): [{ _id: string }] {
  return [{ _id }]
}
