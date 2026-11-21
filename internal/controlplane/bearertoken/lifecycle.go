package bearertoken

import "time"

// Every stored credential has one lifecycle (issue #996, design #995): issued and shown once, active with its last
// use written at most once an hour, then expired or revoked with a reason, never back. The state is the same on each
// kind, as Omnara's machine daemon tokens keep revoked_at and revoke_reason and touch last_used_at hourly
// (identitystore/auth_usage_touch.go) and Coder's api_keys keep expires_at and last_used (httpmw/apikey.go).

// TouchEvery bounds how often a credential's last use is written: Omnara's bearerTokenTouchInterval and Coder's
// hourly last_used.
const TouchEvery = time.Hour

// RevokeReason says why a credential stopped working; it is stored with the time, short and snake_case, as Omnara's
// revoke_reason values ("machine_deleting", "replaced_by_registered_runtime") are.
type RevokeReason string

// The reasons.
const (
	// RevokedByOwner: the owner revoked it by hand.
	RevokedByOwner RevokeReason = "owner"
	// RevokedParentRevoked: the coordinator token it was made under was revoked (Orca revokes children recursively).
	RevokedParentRevoked RevokeReason = "parent_revoked"
	// RevokedNodeRemoved: the node its Dispatch ran on, or the node it belongs to, was removed (Omnara revokes daemon
	// tokens with their machine).
	RevokedNodeRemoved RevokeReason = "node_removed"
	// RevokedChatDeleted: the chat it was made for was deleted.
	RevokedChatDeleted RevokeReason = "chat_deleted"
	// RevokedBotDeleted: the Bot it was made for was deleted.
	RevokedBotDeleted RevokeReason = "bot_deleted"
	// RevokedDispatchEnded: its Dispatch settled, so no worker is left to use it.
	RevokedDispatchEnded RevokeReason = "dispatch_ended"
	// RevokedProcessEnded: the pi it was made for ended.
	RevokedProcessEnded RevokeReason = "process_ended"
	// RevokedReplaced: a newer one was made for the same Dispatch or chat.
	RevokedReplaced RevokeReason = "replaced"
	// RevokedRestarted: the control plane started again, and no process that held it outlives it.
	RevokedRestarted RevokeReason = "control_plane_started"
	// RevokedUsageOnly: made only to record usage on, revoked as it was made.
	RevokedUsageOnly RevokeReason = "usage_only"
	// RevokedSignedOut: its browser signed out.
	RevokedSignedOut RevokeReason = "signed_out"
	// RevokedPasswordChanged: its user's password changed.
	RevokedPasswordChanged RevokeReason = "password_changed"
)
