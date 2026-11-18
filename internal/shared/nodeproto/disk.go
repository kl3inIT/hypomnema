package nodeproto

type DiskSpace struct {
	FreeBytes  *int64 `json:"free_bytes,omitempty"`
	FloorBytes int64  `json:"floor_bytes"`
	TrimBytes  int64  `json:"trim_bytes"`
	SampleAt   int64  `json:"sample_at,omitempty"`
}

func (d DiskSpace) AllowsWork() bool {
	return d.FreeBytes != nil && *d.FreeBytes >= d.FloorBytes
}

func (d DiskSpace) Valid() bool {
	return d.FloorBytes > 0 && d.TrimBytes >= d.FloorBytes && (d.FreeBytes == nil || *d.FreeBytes >= 0)
}

const CleanupBatchSize = 32

type WorktreePath struct {
	RunID  string `json:"run_id"`
	TaskID string `json:"task_id"`
	Path   string `json:"path"`
}

type CleanupCheck struct {
	ID    string         `json:"id"`
	Paths []WorktreePath `json:"paths"`
}

type CleanupWorktree struct {
	WorktreePath
	HasRepo    bool     `json:"has_repo"`
	Dispatches []string `json:"dispatches"`
	Accepted   []string `json:"accepted,omitempty"`
}

type CleanupAck struct {
	ID        string            `json:"id"`
	Worktrees []CleanupWorktree `json:"worktrees"`
	Protected *[]string         `json:"protected,omitempty"`
}
