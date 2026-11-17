package meetings

import (
	"context"
	"errors"
	"fmt"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/meetingstore"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/storeerr"
)

// The notes formats (issues #721, #747): each is a list of sections fed to the prompt, a title and what it holds, as
// Hyprnote's templates are sections of a title and a description fed to its enhance prompt and Meetily's are
// sections of a title, an instruction and a format. The summary, the decisions and the actions are written for
// every format (MemoryOS TranscriptSummarizer, Nojoin meeting_notes.py), so a format lists only what it adds: a
// section that only lists the actions or the decisions (Action Items, Next Steps, Board Decisions, Decisions Made…)
// is left out of the copies, and so are Meetily's sections that hold what the meeting already records (its date,
// attendees, a reviewer's flag). The owner's own formats are kept by meetingstore; built-in ones are copied, not
// changed, as Hyprnote's built-in templates are duplicated to be edited (apps/desktop/src/templates/details.tsx).
//
// The sections are copied, cut down as said, from:
//
//   - Hyprnote (crates/db-app/migrations/20260524000000_default_templates.sql: its 17 default templates),
//     MIT License, Copyright (c) 2023-present Fastrepl, Inc.;
//   - Meetily (frontend/src-tauri/templates/standard_meeting.json, project_sync.json,
//     sales_marketing_client_call.json, retrospective.json, psychatric_session.json; daily_standup.json is
//     Hyprnote's Daily Standup), MIT License, Copyright (c) 2024 Zackriya Solutions.
//
// Permission is hereby granted, free of charge, to any person obtaining a copy of this software and associated
// documentation files (the "Software"), to deal in the Software without restriction, including without limitation
// the rights to use, copy, modify, merge, publish, distribute, sublicense, and/or sell copies of the Software, and to
// permit persons to whom the Software is furnished to do so, subject to the following conditions: The above copyright
// notice and this permission notice shall be included in all copies or substantial portions of the Software. THE
// SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR IMPLIED, INCLUDING BUT NOT LIMITED TO THE
// WARRANTIES OF MERCHANTABILITY, FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE AUTHORS
// OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR
// OTHERWISE, ARISING FROM, OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE SOFTWARE.

// Format is a notes format.
type Format = meetingstore.Format

// FormatSection is one section a format adds to the notes.
type FormatSection = meetingstore.FormatSection

// DefaultFormat is the format of a meeting uploaded without one, and of notes whose format was deleted.
const DefaultFormat = "general"

type sec = FormatSection

var builtins = shipped([]Format{
	{ID: "general", Name: "General", Sections: []sec{
		// Meetily standard_meeting.json.
		{Title: "Discussion highlights", About: "The main topics of discussion, key arguments, and important insights."},
	}},
	{ID: "team", Name: "Team meeting", Sections: []sec{
		// Meetily project_sync.json.
		{Title: "Progress summary", About: "Progress since the last meeting, in a short paragraph."},
		{Title: "Milestones & status", About: "Current milestones with their status and expected completion."},
		{Title: "Top risks & mitigations", About: "The top risks with their impact and how they are handled."},
	}},
	{ID: "one_on_one", Name: "1:1 Meeting", Sections: []sec{
		// Hyprnote 1:1 Meeting.
		{Title: "Updates", About: "What's been happening since last time?"},
		{Title: "Wins & Challenges", About: "Celebrate successes and discuss obstacles"},
		{Title: "Goals & Progress", About: "Review current goals and track progress"},
		{Title: "Feedback", About: "Two-way feedback exchange"},
	}},
	{ID: "standup", Name: "Daily Standup", Sections: []sec{
		// Hyprnote Daily Standup; Meetily's daily_standup.json has the same three.
		{Title: "Yesterday's Accomplishments", About: "What did you complete yesterday?"},
		{Title: "Today's Plan", About: "What are you working on today?"},
		{Title: "Blockers", About: "Any obstacles or help needed?"},
		{Title: "Team Updates", About: "Important announcements or information"},
	}},
	{ID: "client_call", Name: "Client call", Sections: []sec{
		// Meetily sales_marketing_client_call.json.
		{Title: "Client goals & success criteria", About: "What the client wants to achieve and how success is measured."},
		{Title: "Agreed deliverables", About: "Deliverables, owners and due dates."},
		{Title: "Commercial terms discussed", About: "Pricing, SLAs, payment terms or contract items discussed."},
		{Title: "Risks & concerns", About: "Client concerns, blockers or escalation items."},
	}},
	// Hyprnote's other default templates, by name.
	{ID: "board_meeting", Name: "Board Meeting", Sections: []sec{
		{Title: "Company Performance", About: "Financial and operational updates"},
		{Title: "Strategic Initiatives", About: "Progress on key strategic priorities"},
		{Title: "Financial Review", About: "Budget, runway, and financial health"},
		{Title: "Risk & Compliance", About: "Risk management and compliance updates"},
	}},
	{ID: "brainstorming_session", Name: "Brainstorming Session", Sections: []sec{
		{Title: "Session Goal", About: "What problem or opportunity are we exploring?"},
		{Title: "Ideas Generated", About: "All ideas captured during brainstorming"},
		{Title: "Promising Concepts", About: "Ideas worth exploring further"},
		{Title: "Evaluation Criteria", About: "How we'll assess ideas"},
	}},
	{ID: "client_kickoff", Name: "Client Kickoff Meeting", Sections: []sec{
		{Title: "Project Overview", About: "Scope, objectives, and deliverables"},
		{Title: "Team Introductions", About: "Roles and responsibilities"},
		{Title: "Timeline & Milestones", About: "Project schedule and key dates"},
		{Title: "Communication Plan", About: "Meeting cadence and updates"},
		{Title: "Success Criteria", About: "How we measure project success"},
	}},
	{ID: "customer_discovery", Name: "Customer Discovery Interview", Sections: []sec{
		{Title: "Background", About: "Customer context and background"},
		{Title: "Current Process", About: "How they currently solve the problem"},
		{Title: "Pain Points", About: "Challenges and frustrations"},
		{Title: "Needs & Goals", About: "What they're trying to achieve"},
		{Title: "Feedback on Solution", About: "Reactions to proposed solution"},
		{Title: "Key Insights", About: "Main takeaways and learnings"},
	}},
	{ID: "executive_briefing", Name: "Executive Briefing", Sections: []sec{
		{Title: "Strategic Overview", About: "High-level context and objectives"},
		{Title: "Key Metrics", About: "Performance indicators and trends"},
		{Title: "Resource Allocation", About: "Budget and resource commitments"},
		{Title: "Risks & Opportunities", About: "Strategic risks and growth opportunities"},
	}},
	{ID: "incident_postmortem", Name: "Incident Postmortem", Sections: []sec{
		{Title: "Incident Summary", About: "What happened and when"},
		{Title: "Timeline of Events", About: "Chronological sequence of events"},
		{Title: "Root Cause Analysis", About: "What caused the incident"},
		{Title: "Impact Assessment", About: "Effect on users and business"},
		{Title: "Response & Resolution", About: "How the incident was resolved"},
		{Title: "Lessons Learned", About: "Key takeaways and insights"},
	}},
	{ID: "investor_pitch", Name: "Investor Pitch Meeting", Sections: []sec{
		{Title: "Meeting Context", About: "Investor background and meeting purpose"},
		{Title: "Pitch Summary", About: "Key points presented"},
		{Title: "Questions Asked", About: "Investor questions and concerns"},
		{Title: "Feedback Received", About: "Investor reactions and feedback"},
		{Title: "Investment Interest", About: "Level of interest and next steps"},
		{Title: "Follow-up Items", About: "Information requested and action items"},
	}},
	{ID: "lecture_notes", Name: "Lecture Notes", Sections: []sec{
		{Title: "Lecture Information", About: "Course, date, and topic"},
		{Title: "Key Concepts", About: "Main ideas and theories covered"},
		{Title: "Important Definitions", About: "Terms and definitions to remember"},
		{Title: "Examples & Applications", About: "Examples and real-world applications"},
		{Title: "Questions & Clarifications", About: "Items to follow up on"},
	}},
	{ID: "performance_review", Name: "Performance Review", Sections: []sec{
		{Title: "Review Period Summary", About: "Overview of the review period"},
		{Title: "Key Accomplishments", About: "Major achievements and contributions"},
		{Title: "Areas of Strength", About: "Skills and behaviors to continue"},
		{Title: "Areas for Development", About: "Growth opportunities and improvements"},
		{Title: "Career Goals", About: "Career aspirations and development path"},
		{Title: "Action Plan", About: "Development goals and next steps"},
	}},
	{ID: "product_roadmap_review", Name: "Product Roadmap Review", Sections: []sec{
		{Title: "Current Status", About: "Progress on in-flight initiatives"},
		{Title: "Upcoming Features", About: "Next quarter priorities and timeline"},
		{Title: "Customer Feedback", About: "Key insights from users and customers"},
		{Title: "Success Metrics", About: "KPIs and measurement approach"},
		{Title: "Resource Allocation", About: "Team assignments and capacity"},
	}},
	{ID: "project_kickoff", Name: "Project Kickoff", Sections: []sec{
		{Title: "Project Overview"},
		{Title: "Goals & Success Metrics", About: "Define what success looks like"},
		{Title: "Stakeholders & Roles"},
		{Title: "Timeline & Milestones"},
		{Title: "Risks & Dependencies"},
	}},
	{ID: "sales_discovery_call", Name: "Sales Discovery Call", Sections: []sec{
		{Title: "Company Overview", About: "Background on the prospect"},
		{Title: "Current Situation", About: "How they operate today"},
		{Title: "Challenges & Pain Points", About: "Problems they're trying to solve"},
		{Title: "Goals & Success Criteria", About: "What success looks like"},
		{Title: "Budget & Timeline", About: "Financial and timing constraints"},
	}},
	{ID: "sprint_planning", Name: "Sprint Planning", Sections: []sec{
		{Title: "Sprint Goal", About: "What is the main objective for this sprint?"},
		{Title: "Sprint Backlog", About: "Stories and tasks committed for the sprint"},
		{Title: "Capacity & Availability", About: "Team availability and capacity estimates"},
		{Title: "Dependencies & Risks", About: "External dependencies and potential risks"},
		{Title: "Definition of Done", About: "Acceptance criteria and completion standards"},
	}},
	{ID: "sprint_retrospective", Name: "Sprint Retrospective", Sections: []sec{
		{Title: "What Went Well", About: "Celebrate wins and positive outcomes"},
		{Title: "What Didn't Go Well", About: "Identify challenges and problems"},
		{Title: "What We Learned", About: "Key insights and takeaways"},
	}},
	{ID: "technical_design_review", Name: "Technical Design Review", Sections: []sec{
		{Title: "Problem Statement", About: "What problem are we solving?"},
		{Title: "Proposed Solution", About: "Technical approach and architecture"},
		{Title: "Alternatives Considered", About: "Other options and trade-offs"},
		{Title: "Implementation Plan", About: "Breakdown of work and timeline"},
		{Title: "Testing Strategy", About: "How we'll validate the solution"},
		{Title: "Risks & Mitigation", About: "Potential issues and contingency plans"},
	}},
	// Meetily's that differ from Hyprnote's.
	{ID: "retrospective_start_stop", Name: "Retrospective (start, stop, continue)", Sections: []sec{
		// Meetily retrospective.json.
		{Title: "Start Doing", About: "Actions or experiments to start next sprint"},
		{Title: "Stop Doing", About: "Practices to stop"},
		{Title: "Continue Doing", About: "Practices to continue"},
		{Title: "Notes & Votes", About: "Summary and top-voted items"},
	}},
	{ID: "psychiatric_session", Name: "Psychiatric Session Note (SOAP)", Sections: []sec{
		// Meetily psychatric_session.json.
		{Title: "Subjective (S)", About: "Patient's self-reported mood, symptoms, sleep, appetite, stressors"},
		{Title: "Objective (O)", About: "Observable findings: affect, speech, behavior, orientation, vitals if relevant"},
		{Title: "Assessment (A)", About: "Diagnostic impression, risk assessment (SI/HI), progress vs treatment goals"},
		{Title: "Plan (P)", About: "Interventions, medication changes, referrals, therapy tasks and follow-up"},
		{Title: "Medications", About: "Current medications, doses, changes, and rationale"},
		{Title: "Diagnoses (DSM/ICD)", About: "List diagnostic codes and working diagnosis"},
		{Title: "Safety & Risk Management", About: "Safety plan, emergency contacts, hospitalization considerations"},
		{Title: "Next Appointment", About: "Date, time and modality of next session"},
	}},
})

// shipped marks formats built in.
func shipped(formats []Format) []Format {
	for i := range formats {
		formats[i].Builtin = true
	}

	return formats
}

// Builtins are the formats the control plane ships, in the order offered.
func Builtins() []Format { return builtins }

func builtin(id string) (Format, bool) {
	for _, f := range builtins {
		if f.ID == id {
			return f, true
		}
	}

	return Format{}, false
}

// Formats are every notes format: the built-in ones, then the owner's.
func (s *Service) Formats(ctx context.Context) ([]Format, error) {
	own, err := s.cfg.Store.Formats(ctx)
	if err != nil {
		return nil, err
	}

	return append(append([]Format{}, builtins...), own...), nil
}

// FormatOf returns a format by its id; false when there is none.
func (s *Service) FormatOf(ctx context.Context, id string) (Format, bool, error) {
	if f, ok := builtin(id); ok {
		return f, true, nil
	}

	if !meetingstore.IsFormatID(id) {
		return Format{}, false, nil
	}

	f, err := s.cfg.Store.Format(ctx, id)
	if errors.Is(err, storeerr.ErrNotFound) {
		return Format{}, false, nil
	}

	if err != nil {
		return Format{}, false, err
	}

	return f, true, nil
}

// checkFormat refuses a format id that names no format.
func (s *Service) checkFormat(ctx context.Context, id string) error {
	_, ok, err := s.FormatOf(ctx, id)
	if err != nil {
		return err
	}

	if !ok {
		return fmt.Errorf("%w: no notes format %q", ErrInvalid, id)
	}

	return nil
}

// CreateFormat keeps a format the owner made.
func (s *Service) CreateFormat(ctx context.Context, name string, sections []FormatSection) (Format, error) {
	return s.cfg.Store.CreateFormat(ctx, name, sections)
}

// UpdateFormat changes a format the owner made; a built-in one is refused.
func (s *Service) UpdateFormat(ctx context.Context, id, name string, sections []FormatSection) (Format, error) {
	return s.cfg.Store.UpdateFormat(ctx, id, name, sections)
}

// DeleteFormat removes a format the owner made; a built-in one is refused.
func (s *Service) DeleteFormat(ctx context.Context, id string) error {
	return s.cfg.Store.DeleteFormat(ctx, id)
}
