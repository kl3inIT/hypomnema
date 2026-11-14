package clientapi

import (
	"context"
	"errors"
	"fmt"

	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
)

// Who may call what (issue #41, section 3). Every token reads everything and
// may create a Run; a coordinator token creates the Runs it coordinates. A
// change to a Run, its Tasks or its questions needs the owner or the Run's
// coordinator. So does reading a worker's transcript (issue #43): it holds
// what the worker's tools printed, as a chat holds what the owner wrote. Omnara keeps one policy per operationId the same way
// (openapi_policies.go).

// scope says which Run an operation changes, or reads in the transcript's case.
type scope int

const (
	// scopeNone: reads, and creating a Run.
	scopeNone scope = iota
	scopeRun
	scopeTask
	scopeQuestion
	scopeDispatch
	// scopeOwner: only the owner's tokens.
	scopeOwner
)

type policy struct {
	scope scope
	// long: the request may outlive the server's write timeout.
	long bool
	// public: the request needs no token and no session; signing in is the one.
	public bool
}

var policies = map[string]policy{
	"GetMe":                       {},
	"CreateSession":               {public: true},
	"DeleteSession":               {},
	"ListRuns":                    {},
	"CreateRun":                   {},
	"GetRun":                      {},
	"FinishRun":                   {scope: scopeRun},
	"CancelRun":                   {scope: scopeRun},
	"CreateTasks":                 {scope: scopeRun},
	"ListTasks":                   {},
	"GetTask":                     {},
	"ListDispatchItems":           {scope: scopeDispatch, long: true},
	"CancelTask":                  {scope: scopeTask},
	"ReopenTask":                  {scope: scopeTask},
	"ListQuestions":               {},
	"GetQuestion":                 {},
	"AnswerQuestion":              {scope: scopeQuestion},
	"ForwardQuestion":             {scope: scopeQuestion},
	"ListEvents":                  {long: true},
	"StreamEvents":                {long: true},
	"ListNodes":                   {},
	"CreateNode":                  {scope: scopeOwner},
	"ListMachineProviders":        {scope: scopeOwner},
	"UpdateNode":                  {scope: scopeOwner},
	"OpenNodeTerminal":            {scope: scopeOwner},
	"GetBrowser":                  {scope: scopeOwner},
	"OpenBrowserView":             {scope: scopeOwner},
	"ListChatWorkspaces":          {scope: scopeOwner},
	"ListWorkspaceDirectory":      {scope: scopeOwner},
	"ReadWorkspaceFile":           {scope: scopeOwner},
	"ListWorkspaceChanges":        {scope: scopeOwner},
	"DiffWorkspaceFile":           {scope: scopeOwner},
	"WriteWorkspaceFile":          {scope: scopeOwner},
	"DeleteWorkspaceEntry":        {scope: scopeOwner},
	"ReadWorkspaceRaw":            {scope: scopeOwner},
	"ReadWorkspaceSheets":         {scope: scopeOwner},
	"MakeWorkspaceFolder":         {scope: scopeOwner},
	"MoveWorkspaceEntry":          {scope: scopeOwner},
	"BeginWorkspaceUpload":        {scope: scopeOwner},
	"SendWorkspaceUploadChunk":    {scope: scopeOwner},
	"CommitWorkspaceUpload":       {scope: scopeOwner},
	"AbortWorkspaceUpload":        {scope: scopeOwner},
	"StopNodeMachine":             {scope: scopeOwner},
	"StartNodeMachine":            {scope: scopeOwner},
	"RemoveNode":                  {scope: scopeOwner},
	"DrainNode":                   {scope: scopeOwner},
	"ResumeNode":                  {scope: scopeOwner},
	"ListGitHosts":                {},
	"PutGitHost":                  {scope: scopeOwner},
	"DeleteGitHost":               {scope: scopeOwner},
	"PutGitHostToken":             {scope: scopeOwner},
	"DeleteGitHostToken":          {scope: scopeOwner},
	"ListIntegrations":            {},
	"ListSkills":                  {},
	"GetSkill":                    {},
	"GetSkillArchive":             {},
	"ListSkillFiles":              {},
	"ListBuiltinSkills":           {},
	"ListBotSkills":               {},
	"AddSkill":                    {scope: scopeOwner, long: true},
	"UploadSkill":                 {scope: scopeOwner},
	"SetSkillEveryone":            {scope: scopeOwner},
	"DeleteSkill":                 {scope: scopeOwner},
	"SearchSkills":                {scope: scopeOwner},
	"ListSkillAudits":             {scope: scopeOwner},
	"ListSecrets":                 {scope: scopeOwner},
	"GetSecretValue":              {scope: scopeOwner},
	"PutSecret":                   {scope: scopeOwner},
	"DeleteSecret":                {scope: scopeOwner},
	"PutGitHostConnection":        {scope: scopeOwner},
	"DeleteGitHostConnection":     {scope: scopeOwner},
	"PutIntegrationClient":        {scope: scopeOwner},
	"BeginIntegrationSignIn":      {scope: scopeOwner},
	"CompleteIntegrationSignIn":   {scope: scopeOwner},
	"PutIntegrationConnection":    {scope: scopeOwner},
	"DeleteIntegrationConnection": {scope: scopeOwner},
	"ListLarkChannels":            {scope: scopeOwner},
	"GetLarkChannel":              {scope: scopeOwner},
	"PutLarkChannel":              {scope: scopeOwner},
	"DeleteLarkChannel":           {scope: scopeOwner},
	"AddLarkChannelPerson":        {scope: scopeOwner},
	"RemoveLarkChannelPerson":     {scope: scopeOwner},
	"SetLarkChannelGroup":         {scope: scopeOwner},
	"PatchIntegrationConnection":  {scope: scopeOwner},
	"ListConnectionActions":       {scope: scopeOwner},
	"SetConnectionAction":         {scope: scopeOwner},

	// Routines (issue #211). Every token reads them. A Bot, with its pi's token, makes or changes one of its own
	// from its chat and runs it once; a Routine's script's token does neither (routines.go). The launcher, with
	// the coordinator's token, is given a run's script once. The rest is the owner's.
	"ListRoutines":           {},
	"GetRoutine":             {},
	"ListRoutineRuns":        {},
	"PreviewRoutineSchedule": {},
	"PutRoutine":             {},
	"RunRoutine":             {},
	"LaunchRoutineScript":    {},
	"UndoRoutine":            {scope: scopeOwner},
	"DeleteRoutine":          {scope: scopeOwner},
	"PauseRoutine":           {scope: scopeOwner},
	"ResumeRoutine":          {scope: scopeOwner},
	"RevokeRoutineGrant":     {scope: scopeOwner},

	// Superset (issue #203). The owner, or a coordinator when the Connection is on for chats: the handler decides
	// (superset.go). A request may wait for the owner's Approval, longer than the write timeout.
	"ListSupersetDashboards": {long: true},
	"GetSupersetDashboard":   {long: true},
	"GetSupersetChartData":   {long: true},
	"RelaySupersetRequest":   {long: true},

	"ListApprovals":           {scope: scopeOwner},
	"DecideApproval":          {scope: scopeOwner},
	"ListRepos":               {},
	"PutRepo":                 {scope: scopeOwner},
	"DeleteRepo":              {scope: scopeOwner},
	"ListAccounts":            {},
	"AddTokenAccount":         {scope: scopeOwner},
	"EnableAccount":           {scope: scopeOwner},
	"DisableAccount":          {scope: scopeOwner},
	"RemoveAccount":           {scope: scopeOwner},
	"GetAccountLimits":        {scope: scopeOwner},
	"RedeemAccountLimitReset": {scope: scopeOwner},
	"BeginAccountSignIn":      {scope: scopeOwner},
	// Completing may wait for the owner to enter a code at the vendor.
	"CompleteAccountSignIn":     {scope: scopeOwner, long: true},
	"MoveAccount":               {scope: scopeOwner},
	"ListGatewayModels":         {},
	"SetGatewayModelAttributes": {scope: scopeOwner},
	"ListGatewayKeys":           {scope: scopeOwner},
	"GetGatewayUsage":           {scope: scopeOwner},
	"ListGatewayRequests":       {scope: scopeOwner},
	"CreateGatewayKey":          {scope: scopeOwner},
	"RevokeGatewayKey":          {scope: scopeOwner},
	"ListBots":                  {},
	"GetBot":                    {},
	// The owner, or the Bot coordinator from its chat: the handler decides (setupchanges.go).
	"PutBot":              {},
	"DeleteBot":           {},
	"ChangeBotMemory":     {},
	"SetBotMemory":        {scope: scopeOwner},
	"ListProfiles":        {},
	"GetProfile":          {},
	"ListProfileVersions": {},
	"ApplyProfile":        {},
	"ListSetupChanges":    {scope: scopeOwner},
	"UndoSetupChange":     {scope: scopeOwner},
	// Memories (issue #21): every token reads them; a Bot keeps entries about the owner; only the owner changes
	// an agent's, whose workers write it through the agent API, and replaces a whole memory.
	"GetAgentMemory":    {},
	"SetAgentMemory":    {scope: scopeOwner},
	"ChangeAgentMemory": {scope: scopeOwner},
	"GetOwnerMemory":    {},
	"SetOwnerMemory":    {scope: scopeOwner},
	"ChangeOwnerMemory": {},
	"ListProviders":     {},
	"ApplyProvider":     {scope: scopeOwner},
	"ListModels":        {},
	// Artifacts (issue #402): every token reads them, a Bot's only its own chat's; only a Bot's shows one, and
	// only the owner's web opens a folder page's frame (artifacts.go).
	"ListArtifacts":      {},
	"ShowArtifact":       {},
	"GetArtifact":        {},
	"ReadArtifactFile":   {},
	"GetArtifactArchive": {},
	"OpenArtifactFrame":  {scope: scopeOwner},
	// Only the owner's web previews a workbook (issue #422, sheets.go).
	"ReadArtifactSheets": {scope: scopeOwner},
	"ListChats":          {scope: scopeOwner},
	"CreateChat":         {scope: scopeOwner},
	"GetChat":            {scope: scopeOwner},
	"UpdateChat":         {scope: scopeOwner},
	"ListChatMessages":   {scope: scopeOwner, long: true},
	"StreamChat":         {scope: scopeOwner, long: true},
	"AbortChat":          {scope: scopeOwner},
	"CompactChat":        {scope: scopeOwner},
	// pi reads its own key (issue #204). The handler lets one token through, pi's, by its name: no scope here
	// says that.
	"GetChatGatewayKey": {},
	"SendChatMessage":   {scope: scopeOwner},
	// The web (issue #354): any token, a Bot's above all. A search may try every provider, longer than the
	// write timeout at worst.
	"SearchWeb": {long: true},
	"FetchWeb":  {long: true},
	// The web search chain (issues #354, #393): every token reads it, as it reads the accounts; only the owner changes it.
	"GetWebSearch":         {},
	"SetWebSearchProvider": {scope: scopeOwner},
	"SetWebSearchOrder":    {scope: scopeOwner},
	"AddWebSearchKey":      {scope: scopeOwner},
	"RemoveWebSearchKey":   {scope: scopeOwner},
	// Images (issue #431): any token makes one, a Bot's above all; one takes a minute or more. Every token reads the
	// providers, as it reads the web search chain; only the owner changes them.
	"GenerateImages":   {long: true},
	"GetImages":        {},
	"SetImageProvider": {scope: scopeOwner},
	"SetImageOrder":    {scope: scopeOwner},
}

var (
	errUnauthorized = errors.New("signing in or a valid bearer token is required")
	errForbidden    = errors.New("forbidden")
)

// checkPolicy refuses a coordinator token a change to a Run it does not
// coordinate. The owner may change anything.
func (s *Server) checkPolicy(ctx context.Context, p policy, tok orchestration.Token, request any) error {
	if p.scope == scopeNone || tok.Kind == bearertoken.Owner {
		return nil
	}

	if p.scope == scopeOwner {
		return fmt.Errorf("%w: only the owner's token may do this", errForbidden)
	}

	runID, err := s.runOf(ctx, p.scope, request)
	if err != nil {
		return err
	}

	run, err := s.svc.GetRun(ctx, runID)
	if err != nil {
		return err
	}

	if run.CoordinatorTokenID != tok.ID {
		return fmt.Errorf("%w: run %s is coordinated by another token or by the owner", errForbidden, runID)
	}

	// A pi's token changes only the Runs of its own chat (issue #260).
	if tok.Kind == bearertoken.Bot && run.ChatID != tok.Chat {
		return fmt.Errorf("%w: run %s belongs to another chat", errForbidden, runID)
	}

	return nil
}

// chatOf is the chat a request acts in. A pi's token acts in its own chat
// (issue #260): a request that names another is refused, and one that names
// none is its chat's. Any other token acts in the chat it names, if any.
func chatOf(tok orchestration.Token, named string) (string, error) {
	if tok.Kind != bearertoken.Bot {
		return named, nil
	}

	if named != "" && named != tok.Chat {
		return "", fmt.Errorf("%w: this token is of chat %s, not of chat %s", errForbidden, tok.Chat, named)
	}

	return tok.Chat, nil
}

// callerName is what the log calls a coordinator: a pi's token by its chat
// and Bot, never by its secret.
func callerName(tok orchestration.Token) string {
	if tok.Kind == bearertoken.Bot && tok.Routine != "" {
		return fmt.Sprintf("bot %s in routine %s", tok.Bot, tok.Routine)
	}

	if tok.Kind == bearertoken.Bot {
		return fmt.Sprintf("bot %s in chat %s", tok.Bot, tok.Chat)
	}

	return "coordinator token " + tok.ID
}

// botAllowed refuses a pi's token anything of a Bot other than its own
// (issue #260): its persona, its memory and its skills.
func botAllowed(tok orchestration.Token, bot string) error {
	if tok.Kind == bearertoken.Bot && bot != tok.Bot {
		return fmt.Errorf("%w: this token is of bot %s, not of bot %s", errForbidden, tok.Bot, bot)
	}

	return nil
}

// botReadable refuses a pi's token to read a Bot other than its own, except
// the Bot coordinator's, which manages the Bots (issue #169, setupchanges.go)
// and reads them as the owner's CLI does before it changes one.
func botReadable(tok orchestration.Token, bot string) error {
	if _, err := setupChat(tok); err == nil {
		return nil
	}

	return botAllowed(tok, bot)
}

// runOf returns the Run a scoped request changes.
func (s *Server) runOf(ctx context.Context, sc scope, request any) (string, error) {
	switch r := request.(type) {
	case openapi.FinishRunRequestObject:
		return r.RunId, nil
	case openapi.CancelRunRequestObject:
		return r.RunId, nil
	case openapi.CreateTasksRequestObject:
		return r.RunId, nil
	case openapi.CancelTaskRequestObject:
		return s.runOfTask(ctx, r.TaskId)
	case openapi.ReopenTaskRequestObject:
		return s.runOfTask(ctx, r.TaskId)
	case openapi.AnswerQuestionRequestObject:
		return s.runOfQuestion(ctx, r.QuestionId)
	case openapi.ForwardQuestionRequestObject:
		return s.runOfQuestion(ctx, r.QuestionId)
	case openapi.ListDispatchItemsRequestObject:
		d, err := s.svc.GetDispatch(ctx, r.DispatchId)
		if err != nil {
			return "", err
		}

		return s.runOfTask(ctx, d.TaskID)
	default:
		return "", fmt.Errorf("scope %d: no run for a %T", sc, request)
	}
}

func (s *Server) runOfTask(ctx context.Context, id string) (string, error) {
	t, err := s.svc.GetTask(ctx, id)

	return t.RunID, err
}

func (s *Server) runOfQuestion(ctx context.Context, id string) (string, error) {
	q, err := s.svc.GetQuestion(ctx, id)

	return q.RunID, err
}
