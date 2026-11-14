package clientapi

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/spreadsheet"
	"github.com/kl3inIT/hypomnema/internal/shared/workspacefs"
)

// A workbook's preview (issue #422): the web shows an xlsx file as tables, one a sheet, and ships no workbook
// parser, so the control plane reads it into CSV text a sheet (spreadsheet.Read), as MemoryOS's API does for a
// Chat file and a Document original. A Bot's artifact is read where it is kept; a file of a chat's folder is read
// from the machine that holds the folder as /raw reads it, its bytes parsed here.

// ReadArtifactSheets reads a revision's workbook as CSV text a sheet.
func (s *Server) ReadArtifactSheets(ctx context.Context, req openapi.ReadArtifactSheetsRequestObject,
) (openapi.ReadArtifactSheetsResponseObject, error) {
	if _, err := s.readableArtifact(ctx, req.ArtifactId); err != nil {
		return nil, err
	}

	f, err := s.svc.ReadArtifactFile(ctx, req.ArtifactId, req.Revision, val(req.Params.Path))
	if err != nil {
		return nil, err
	}

	sheets, err := readSheets(ctx, f.Data)
	if err != nil {
		return nil, err
	}

	return openapi.ReadArtifactSheets200JSONResponse(sheets), nil
}

// ReadWorkspaceSheets reads a workbook of a chat's folder as CSV text a sheet.
func (s *Server) ReadWorkspaceSheets(ctx context.Context, req openapi.ReadWorkspaceSheetsRequestObject,
) (openapi.ReadWorkspaceSheetsResponseObject, error) {
	res, err := s.ask(ctx, req.ChatId, req.WorkspaceId, "", workspacefs.Request{
		Op: workspacefs.OpRead, Path: deref(req.Params.Path), Raw: true,
	})
	if err != nil {
		return nil, err
	}

	if res.File == nil {
		return nil, errors.New("the machine answered without a file")
	}

	sheets, err := readSheets(ctx, res.File.Data)
	if err != nil {
		return nil, err
	}

	return openapi.ReadWorkspaceSheets200JSONResponse(sheets), nil
}

// sheetsTimeout bounds how long a workbook is read: this server's own bound (issue #422, the security review), as the
// read is CPU spent on a file anyone with a chat's folder could put there.
const sheetsTimeout = 20 * time.Second

func readSheets(ctx context.Context, data []byte) (openapi.SheetList, error) {
	ctx, cancel := context.WithTimeout(ctx, sheetsTimeout)
	defer cancel()

	wb, err := spreadsheet.Read(ctx, data)
	if errors.Is(err, context.DeadlineExceeded) {
		return openapi.SheetList{}, fmt.Errorf("%w: the workbook took longer than %s to read; download it instead",
			orchestration.ErrUnavailable, sheetsTimeout)
	}

	if ctx.Err() != nil {
		return openapi.SheetList{}, ctx.Err()
	}

	// Otherwise Read refuses only what is not a workbook it can read: the file's fault, never the server's.
	if err != nil {
		return openapi.SheetList{}, fmt.Errorf("%w: %w", orchestration.ErrInvalid, err)
	}

	out := openapi.SheetList{Sheets: make([]openapi.Sheet, 0, len(wb.Sheets)), Truncated: wb.Truncated}
	for _, sh := range wb.Sheets {
		out.Sheets = append(out.Sheets, openapi.Sheet{Name: sh.Name, Csv: sh.CSV, Truncated: sh.Truncated})
	}

	return out, nil
}
