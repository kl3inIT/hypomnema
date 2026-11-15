// everyCall matches every cached call of one generated operation, whatever its path, query or base URL: a
// generated query key is one object named by the operation's _id, and TanStack Query matches a key by its
// prefix, so the _id alone matches them all (MemoryOS web/src/features/chat/chat-api.ts, everyCall). An exact
// generated key built with `query: {}` does not match one built without `query`; this one matches both.
export function everyCall([{ _id }]: readonly [{ _id: string }]): [{ _id: string }] {
  return [{ _id }]
}

// callsOf matches every cached call of several generated operations by the _id in the key's first entry: a
// predicate, since no one key prefix covers them (Omnara frontend/packages/react/src/domains/skills.ts,
// invalidateSkillLists, with its query-keys.ts generatedQueryKey). A hand-written key does not match.
export function callsOf(...keys: readonly (readonly [{ _id: string }])[]) {
  const ids = new Set(keys.map(([{ _id }]) => _id))

  return ({ queryKey: [first] }: { queryKey: readonly unknown[] }) =>
    typeof first === "object" && first !== null && "_id" in first && ids.has(String(first._id))
}
