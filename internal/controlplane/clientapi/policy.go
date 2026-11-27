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
	// scopeActor: a token that acts as someone, the owner's or a Bot's; a coordinator's acts as no one (the Inbox's
	// writes, issue #966).
	scopeActor
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
	"GetGitHubApp":                {scope: scopeOwner},
	"SyncGitHubAppInstallations":  {scope: scopeOwner},
	"TestGitHubAppDelivery":       {scope: scopeOwner},
	"WaitPullRequestCI":           {long: true},
	"CreateSession":               {public: true},
	"DeleteSession":               {},
	"ListRuns":                    {},
	"CreateRun":                   {},
	"AdoptRun":                    {scope: scopeOwner},
	"GetRun":                      {},
	"FinishRun":                   {scope: scopeRun},
	"CancelRun":                   {scope: scopeRun},
	"CreateTasks":                 {scope: scopeRun},
	"GetRoleRouting":              {},
	"PutRoleRouting":              {scope: scopeOwner},
	"GetRoleMetrics":              {scope: scopeOwner},
	"ListTasks":                   {},
	"GetTask":                     {},
	"ListDispatchItems":           {scope: scopeDispatch, long: true},
	"CancelTask":                  {scope: scopeTask},
	"ReopenTask":                  {scope: scopeTask},
	"FollowUpTask":                {scope: scopeTask},
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
	"GetDevice":                   {scope: scopeOwner},
	"OpenDeviceView":              {scope: scopeOwner},
	"StartNodeDevice":             {scope: scopeOwner},
	"StopNodeDevice":              {scope: scopeOwner},
	"ListTakes":                   {scope: scopeOwner},
	"GetTake":                     {scope: scopeOwner},
	"DeleteTake":                  {scope: scopeOwner},
	"ReadTakeSegment":             {scope: scopeOwner, long: true},
	"GetTakeArchive":              {scope: scopeOwner, long: true},
	"ReadTakeThumbnail":           {scope: scopeOwner},
	"ListStudioItems":             {scope: scopeOwner},
	"ListVideos":                  {scope: scopeOwner},
	"MakeVideo":                   {scope: scopeOwner},
	"GetVideo":                    {scope: scopeOwner},
	"DeleteVideo":                 {scope: scopeOwner},
	"ApproveVideoRevision":        {scope: scopeOwner},
	"RequestVideoChanges":         {scope: scopeOwner},
	"ReadVideoFile":               {scope: scopeOwner, long: true},
	"ReadVideoThumbnail":          {scope: scopeOwner},
	"ChangeVideoLook":             {scope: scopeOwner},
	"RetryVideoRevision":          {scope: scopeOwner},
	"ListVideoLooks":              {scope: scopeOwner},
	"ListVideoTemplates":          {scope: scopeOwner},
	"PreviewVideoLook":            {scope: scopeOwner},
	"ListVideoMusic":              {scope: scopeOwner},
	"ReadVideoMusicAudio":         {scope: scopeOwner, long: true},
	"DeleteVideoMusic":            {scope: scopeOwner},
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
	"ListIntegrations":            {},
	"ListSkills":                  {},
	"GetSkill":                    {},
	"GetSkillArchive":             {},
	"ListSkillFiles":              {},
	"ListBuiltinSkills":           {},
	"ListBotSkills":               {},
	"ListBotTools":                {},
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
	"CompleteIntegrationSignIn":   {scope: scopeOwner, long: true},
	"ListMessagingChats":          {scope: scopeOwner},
	"ListMessagingMessages":       {scope: scopeOwner},
	"BackfillMessagingChat":       {scope: scopeOwner, long: true},
	"SyncMessagingFromPhone":      {scope: scopeOwner, long: true},
	"ReadMessagingMedia":          {scope: scopeOwner, long: true},
	"GetIntegrationSignIn":        {scope: scopeOwner},
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
	"CheckIntegrationConnection":  {scope: scopeOwner},
	"SetConnectionAction":         {scope: scopeOwner},

	// Routines (issue #211). Every token reads them. A Bot, with its pi's token, makes or changes one of its own
	// from its chat and runs it once; a Routine's script's token does neither (routines.go). The launcher, with
	// the coordinator's token, is given a run's script once. The rest is the owner's.
	"ListRoutines":           {},
	"GetRoutine":             {},
	"ListRoutineRuns":        {},
	"PreviewRoutineSchedule": {},
	"PutRoutine":             {},
	"PatchRoutine":           {scope: scopeOwner},
	"RunRoutine":             {},
	"LaunchRoutineScript":    {},
	"UndoRoutine":            {scope: scopeOwner},
	"DeleteRoutine":          {scope: scopeOwner},
	"PauseRoutine":           {scope: scopeOwner},
	"ResumeRoutine":          {scope: scopeOwner},
	"RevokeRoutineGrant":     {scope: scopeOwner},

	// Superset (issue #203). The owner, or a coordinator when the Connection is on for chats: the handler decides
	// (superset.go). A request may wait for the owner's Approval, longer than the write timeout.
	"ListSupersetDashboards":   {long: true},
	"GetSupersetDashboard":     {long: true},
	"GetSupersetChartData":     {long: true},
	"ListSupersetFilterValues": {long: true},
	"RelaySupersetRequest":     {long: true},

	"ListApprovals":  {scope: scopeOwner},
	"DecideApproval": {scope: scopeOwner},
	// A Bot's requests for a Connection (issue #471, part D): only the owner answers them (connectionrequests.go).
	"ListConnectionRequests":  {scope: scopeOwner},
	"DecideConnectionRequest": {scope: scopeOwner},
	"ListRepos":               {},
	"PutRepo":                 {scope: scopeOwner},
	"DeleteRepo":              {scope: scopeOwner},
	"GetGitSSHKey":            {scope: scopeOwner},
	// Web Push (issue #1047): only the owner's own devices subscribe (owner, 2026-11-21).
	"ListPushSubscriptions":   {scope: scopeOwner},
	"PutPushSubscription":     {scope: scopeOwner},
	"DeletePushSubscription":  {scope: scopeOwner},
	"TestPushSubscription":    {scope: scopeOwner},
	"ListAccounts":            {},
	"AddTokenAccount":         {scope: scopeOwner},
	"EnableAccount":           {scope: scopeOwner},
	"DisableAccount":          {scope: scopeOwner},
	"RemoveAccount":           {scope: scopeOwner},
	"GetAccountLimits":        {scope: scopeOwner},
	"RedeemAccountLimitReset": {scope: scopeOwner},
	"BeginAccountSignIn":      {scope: scopeOwner},
	"ImportAccount":           {scope: scopeOwner, long: true},
	// Completing may wait for the owner to enter a code at the vendor.
	"CompleteAccountSignIn":     {scope: scopeOwner, long: true},
	"MoveAccount":               {scope: scopeOwner},
	"ListGatewayModels":         {},
	"SetGatewayModelAttributes": {scope: scopeOwner},
	"ListGatewayKeys":           {scope: scopeOwner},
	"GetGatewayUsage":           {scope: scopeOwner},
	"ListGatewayRequests":       {scope: scopeOwner},
	"CreateGatewayKey":          {scope: scopeOwner},
	"UpdateGatewayKey":          {scope: scopeOwner},
	"GetGatewayKeyValue":        {scope: scopeOwner},
	"GetGatewayToolConfig":      {scope: scopeOwner},
	"RevokeGatewayKey":          {scope: scopeOwner},
	"ListBots":                  {},
	"GetBot":                    {},
	// The owner, or the main Bot from its chat: the handler decides (setupchanges.go).
	"CreateBot": {},
	"PutBot":    {},
	"RenameBot": {},
	"DeleteBot": {},
	// The order the sidebar lists the Bots in (issue #470) is the owner's.
	"ReorderBots": {scope: scopeOwner},
	// Departments, the sidebar's sections of Bots (issue #471), are the owner's too.
	"ListDepartments":     {},
	"CreateDepartment":    {scope: scopeOwner},
	"ReorderDepartments":  {scope: scopeOwner},
	"RenameDepartment":    {scope: scopeOwner},
	"DeleteDepartment":    {scope: scopeOwner},
	"ChangeBotMemory":     {},
	"SetBotMemory":        {scope: scopeOwner},
	"ListProfiles":        {},
	"GetProfile":          {},
	"ListProfileVersions": {},
	"ApplyProfile":        {},
	// Restoring a version applies its source again, as ApplyProfile (issue #1002).
	"RestoreProfileVersion": {},
	"DeleteProfile":         {scope: scopeOwner},
	"ListSetupChanges":      {scope: scopeOwner},
	"UndoSetupChange":       {scope: scopeOwner},
	// Memories (issue #21): every token reads them; a Bot keeps entries about the owner; only the owner changes
	// an agent's, whose workers write it through the agent API, and replaces a whole memory.
	"GetAgentMemory":    {},
	"SetAgentMemory":    {scope: scopeOwner},
	"ChangeAgentMemory": {scope: scopeOwner},
	"GetOwnerMemory":    {},
	"SetOwnerMemory":    {scope: scopeOwner},
	"ChangeOwnerMemory": {},
	// A memory's notes (issue #589): read as the memory is; only the owner writes or removes one by hand.
	"ListMemoryNotes":  {},
	"GetMemoryNote":    {},
	"PutMemoryNote":    {scope: scopeOwner},
	"DeleteMemoryNote": {scope: scopeOwner},
	"ListProviders":    {},
	"ApplyProvider":    {scope: scopeOwner},
	"DeleteProvider":   {scope: scopeOwner},
	"ListModels":       {},
	"ListTools":        {},
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

	// View-only links (issue #745, artifactshares.go): only the owner makes, lists and revokes them, as only
	// Metabase's admins and Onyx's and Claude's owners do; a link is read by its token alone.
	"CreateArtifactShare": {scope: scopeOwner},
	"ListArtifactShares":  {scope: scopeOwner},
	"ListShares":          {scope: scopeOwner},
	"DeleteArtifactShare": {scope: scopeOwner},
	"GetShare":            {public: true},

	"ListChats":        {scope: scopeOwner},
	"CreateChat":       {scope: scopeOwner},
	"OpenMainChat":     {scope: scopeOwner},
	"GetChat":          {scope: scopeOwner},
	"UpdateChat":       {scope: scopeOwner},
	"DeleteChat":       {scope: scopeOwner},
	"ListChatMessages": {scope: scopeOwner, long: true},
	"StreamChat":       {scope: scopeOwner, long: true},
	"AbortChat":        {scope: scopeOwner},
	"CompactChat":      {scope: scopeOwner},
	// pi reads its own key (issue #204). The handler lets one token through, pi's, by its name: no scope here
	// says that.
	"GetChatGatewayKey":       {},
	"SendChatMessage":         {scope: scopeOwner},
	"ListChatQueue":           {scope: scopeOwner},
	"RemoveQueuedChatMessage": {scope: scopeOwner},
	"SendQueuedChatMessage":   {scope: scopeOwner},
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
	// Muse (issue #782): only the owner sees and changes the pool; agents ask it through their tools.
	"GetMuse":           {scope: scopeOwner},
	"SetMuseTools":      {scope: scopeOwner},
	"StartMuseSignIn":   {scope: scopeOwner},
	"CheckMuseSignIn":   {scope: scopeOwner},
	"SetMuseAccount":    {scope: scopeOwner},
	"RemoveMuseAccount": {scope: scopeOwner},
	"MoveMuseAccount":   {scope: scopeOwner},
	// Audio (issue #694): any token hears or makes speech, a Bot's above all; a Soniox transcription waits for its
	// job. Every token reads the providers, as it reads the image providers; only the owner changes them.
	"TranscribeAudio":  {long: true},
	"SpeakAudio":       {long: true},
	"GetAudio":         {},
	"SetAudioProvider": {scope: scopeOwner},
	"SetAudioOrder":    {scope: scopeOwner},
	"SetAudioVoice":    {scope: scopeOwner},
	"SetAudioModel":    {scope: scopeOwner},
	"ListAudioModels":  {},
	"ListAudioVoices":  {},
	"AddAudioKey":      {scope: scopeOwner},
	"RemoveAudioKey":   {scope: scopeOwner},
	// Meetings (issue #721): every token reads them, as it reads the rest; only the owner uploads, deletes or changes
	// one. An upload carries up to 128 MB and a recording is played as long as it is listened to.
	"ListMeetings":          {},
	"UploadMeeting":         {scope: scopeOwner, long: true},
	"GetMeeting":            {},
	"DeleteMeeting":         {scope: scopeOwner},
	"GetMeetingRecording":   {long: true},
	"WriteMeetingNotes":     {scope: scopeOwner},
	"NameMeetingSpeaker":    {scope: scopeOwner},
	"SetMeetingLineSpeaker": {scope: scopeOwner},
	"UpdateMeetingItem":     {scope: scopeOwner},
	"ListMeetingActions":    {},
	"GetMeetingSettings":    {},
	"SetMeetingSettings":    {scope: scopeOwner},
	// A meeting recorded live (issue #820): only the owner records, as only the owner uploads.
	"StartLiveMeeting": {scope: scopeOwner},
	"OpenMeetingTrack": {scope: scopeOwner},
	"EndLiveMeeting":   {scope: scopeOwner, long: true},
	// The words to hear right (issue #747), read and changed as the settings are.
	"FixMeetingWord":   {scope: scopeOwner},
	"ListMeetingWords": {},
	"SetMeetingWords":  {scope: scopeOwner},

	// The Inbox (issue #966): every token reads it; the owner and a Bot write, as the actor its token is, and the
	// store says what each may do (inboxstore's transitions). A coordinator token only reads.
	"ListTodos":        {},
	"GetInboxNeedsYou": {},
	"GetTodo":          {},
	"CreateTodo":       {scope: scopeActor},
	"EditTodo":         {scope: scopeActor},
	"AcceptTodo":       {scope: scopeActor},
	"DoneTodo":         {scope: scopeActor},
	"DropTodo":         {scope: scopeActor},
	"DuplicateTodo":    {scope: scopeActor},
	"ReopenTodo":       {scope: scopeActor},
	"SnoozeTodo":       {scope: scopeActor},
	"StartTodo":        {scope: scopeActor},
	"ReleaseTodo":      {scope: scopeActor},
	// Handing an item to a Bot and sending as the owner are his alone; a Bot comments on its own items (issue #1111).
	"HandTodo":      {scope: scopeOwner},
	"CommentTodo":   {scope: scopeActor},
	"SendTodoDraft": {scope: scopeOwner},
	// Which chats the secretary reads (issue #1270): the owner's own accounts' chats, his alone. A list pages Lark.
	"ListInboxChatSources": {scope: scopeOwner, long: true},
	"ListInboxChats":       {scope: scopeOwner, long: true},
	"SetInboxChat":         {scope: scopeOwner},
	"MoveInboxDropChats":   {scope: scopeOwner, long: true},
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

	if p.scope == scopeActor {
		if tok.Kind == bearertoken.Bot {
			return nil
		}

		return fmt.Errorf("%w: only the owner's token or a Bot's may do this", errForbidden)
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
// the main Bot's, which manages the Bots (issue #169, setupchanges.go)
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
	case openapi.FollowUpTaskRequestObject:
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
