package clientapi

import (
	"bytes"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/xuri/excelize/v2"

	"github.com/kl3inIT/hypomnema/internal/controlplane/artifactpack"
	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
	"github.com/kl3inIT/hypomnema/internal/shared/workspacefs"
)

func salesWorkbook(t *testing.T) []byte {
	t.Helper()

	f := excelize.NewFile()
	defer func() { _ = f.Close() }()

	require.NoError(t, f.SetSheetRow("Sheet1", "A1", &[]any{"region", "total"}))
	require.NoError(t, f.SetSheetRow("Sheet1", "A2", &[]any{"Hà Nội, VN", 120}))

	var buf bytes.Buffer
	require.NoError(t, f.Write(&buf))

	return buf.Bytes()
}

// Scenario: the owner opens a workbook a Bot handed over, and one in a chat's folder (issue #422): each comes back
// as CSV text a sheet, the folder's read from the machine that holds it. A file that is not a workbook is refused
// as invalid, and only the owner's web reads one this way.
func TestAWorkbookIsReadAsCSVASheetForTheOwner(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()
	owner, _ := w.token("web", bearertoken.Owner)
	coord, _ := w.token("pi-coordinator", bearertoken.Coordinator)
	chat, tree := w.chatWithATask(owner)
	pi := w.piToken("pi-coordinator", chat.ID, chat.Bot)
	book := salesWorkbook(t)
	want := []openapi.Sheet{{Name: "Sheet1", Csv: "region,total\n\"Hà Nội, VN\",120\n"}}

	shown := w.show(pi, "Sales", "", artifactpack.File{Path: "sales.xlsx", Data: book})
	require.Equal(t, http.StatusCreated, shown.StatusCode(), string(shown.Body))

	sheets, err := owner.ReadArtifactSheetsWithResponse(ctx, shown.JSON201.Id, 1, &openapi.ReadArtifactSheetsParams{})
	require.NoError(t, err)
	require.NotNil(t, sheets.JSON200, "%s", sheets.Body)
	assert.Equal(t, want, sheets.JSON200.Sheets)

	refused, err := coord.ReadArtifactSheetsWithResponse(ctx, shown.JSON201.Id, 1, &openapi.ReadArtifactSheetsParams{})
	require.NoError(t, err)
	assert.Equal(t, http.StatusForbidden, refused.StatusCode())

	text := w.show(pi, "Notes", "", artifactpack.File{Path: "notes.xlsx", Data: []byte("a,b\n")})
	require.Equal(t, http.StatusCreated, text.StatusCode(), string(text.Body))

	bad, err := owner.ReadArtifactSheetsWithResponse(ctx, text.JSON201.Id, 1, &openapi.ReadArtifactSheetsParams{})
	require.NoError(t, err)
	assert.Equal(t, openapi.InvalidRequest, errorCode(t, bad.Body, bad.StatusCode(), http.StatusBadRequest))

	f := &folders{answer: func(_ string, req workspacefs.Request) workspacefs.Result {
		if !req.Raw {
			return workspacefs.Result{Error: &workspacefs.Error{Code: workspacefs.CodeFailed, Message: "raw only"}}
		}

		return workspacefs.Result{File: &workspacefs.File{Path: req.Path, Bytes: int64(len(book)), Data: book}}
	}}
	w.srv.WithWorkspaces(f.node, f)

	folder, err := owner.ReadWorkspaceSheetsWithResponse(ctx, chat.ID, tree.TaskID,
		&openapi.ReadWorkspaceSheetsParams{Path: ptr("out/sales.xlsx")})
	require.NoError(t, err)
	require.NotNil(t, folder.JSON200, "%s", folder.Body)
	assert.Equal(t, want, folder.JSON200.Sheets)
	assert.Equal(t, []string{"node " + tree.NodeID + " " + tree.Worktree + " read out/sales.xlsx"}, f.asked)

	notOwner, err := coord.ReadWorkspaceSheetsWithResponse(ctx, chat.ID, "bot",
		&openapi.ReadWorkspaceSheetsParams{Path: ptr("sales.xlsx")})
	require.NoError(t, err)
	assert.Equal(t, http.StatusForbidden, notOwner.StatusCode())
}
