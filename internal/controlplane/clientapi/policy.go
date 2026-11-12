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
	"ListBotSkills":               {},
	"AddSkill":                    {scope: scopeOwner, long: true},
	"UploadSkill":                 {scope: scopeOwner},
	"SetSkillEveryone":            {scope: scopeOwner},
	"DeleteSkill":                 {scope: scopeOwner},
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
	"PatchIntegrationConnection":  {scope: scopeOwner},
	"ListConnectionActions":       {scope: scopeOwner},
	"SetConnectionAction":         {scope: scopeOwner},

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
	"CompleteAccountSignIn": {scope: scopeOwner, long: true},
	"MoveAccount":           {scope: scopeOwner},
	"ListGatewayModels":     {},
	"ListGatewayKeys":       {scope: scopeOwner},
	"CreateGatewayKey":      {scope: scopeOwner},
	"RevokeGatewayKey":      {scope: scopeOwner},
	"ListBots":              {},
	"GetBot":                {},
	"PutBot":                {scope: scopeOwner},
	"DeleteBot":             {scope: scopeOwner},
	"ChangeBotMemory":       {},
	"SetBotMemory":          {scope: scopeOwner},
	"ListProfiles":          {},
	"GetProfile":            {},
	"ListProfileVersions":   {},
	"ApplyProfile":          {scope: scopeOwner},
	"ListProviders":         {},
	"ApplyProvider":         {scope: scopeOwner},
	"ListModels":            {},
	"ListChats":             {scope: scopeOwner},
	"CreateChat":            {scope: scopeOwner},
	"GetChat":               {scope: scopeOwner},
	"ListChatMessages":      {scope: scopeOwner, long: true},
	"StreamChat":            {scope: scopeOwner, long: true},
	"AbortChat":             {scope: scopeOwner},
	// pi reads its own key (issue #204). The handler lets one token through, pi's, by its name: no scope here
	// says that.
	"GetChatGatewayKey": {},
	"SendChatMessage":   {scope: scopeOwner},
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

	return nil
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
