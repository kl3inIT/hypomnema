-- +goose Up
-- The pull request the control plane found on a succeeded Task's branch, by its last Dispatch (issue #1168). It is
-- looked up by the branch at the host, as Coder's gitsync resolves a chat's pull request from its branch and keeps
-- its URL (coderd/x/gitsync/gitsync.go:282-343, the url of chat_diff_statuses), so a pull request that merged, and
-- is no longer open on its branch, is still read. url is '' while none was found; since is when it was first looked
-- up: a branch without one is looked up again for a short window from then and dropped after it (Coder's
-- NoPRBackoff and NoPRRetryWindow, coderd/x/gitsync/worker.go:38-53).
CREATE TABLE pull_request_lookups (
    dispatch_id TEXT    NOT NULL PRIMARY KEY REFERENCES dispatches (id),
    url         TEXT    NOT NULL DEFAULT '',
    since       INTEGER NOT NULL
) STRICT, WITHOUT ROWID;

-- +goose Down
DROP TABLE pull_request_lookups;
