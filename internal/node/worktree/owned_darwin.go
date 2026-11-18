package worktree

// Without Linux procfs the supervisor has no ownership evidence to remove a Git lock.
func ownedGitLocks(_ int, _ string) func() { return nil }
