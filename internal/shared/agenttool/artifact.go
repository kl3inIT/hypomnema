package agenttool

// ToolShowArtifact shows a page, a PDF, an image or any file to the owner in
// a chat, as an Artifact (issues #402, #536): one tool for a Bot's pi and a
// worker, after OpenMausBot's show_html (server/drivers/agents-app-tools.ts:
// html or path, a title, app_id to replace). The control plane lists and runs
// it on PathTools and PathToolsCall; the agent names a file or folder of the
// folder it works in, which the tools server on its machine reads into Files
// (internal/shared/toolsmcp/artifact.go), as generate_image's server reads
// the images it sends.
const ToolShowArtifact = "show_artifact"

// ArtifactCall is the arguments of a show_artifact call as the control plane
// takes them: the agent's own (Title, HTML, Path, ID), and the files its
// tools server read from Path. A server of an earlier release passes the
// agent's arguments on unchanged, without Files.
type ArtifactCall struct {
	Title string         `json:"title,omitempty"`
	HTML  string         `json:"html,omitempty"`
	Path  string         `json:"path,omitempty"`
	ID    string         `json:"id,omitempty"`
	Files []ArtifactFile `json:"files,omitempty"`
}

// ArtifactFile is one file of an Artifact: its path relative to the folder
// named, "/" between segments, and its bytes, base64 in JSON.
type ArtifactFile struct {
	Path string `json:"path"`
	Data []byte `json:"data"`
}
