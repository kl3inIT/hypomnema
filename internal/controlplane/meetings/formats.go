package meetings

// The notes formats (issue #721): each is a list of sections fed to the prompt, a title and what it holds, as
// Hyprnote's templates are sections of a title and a description fed to its enhance prompt and Meetily's are
// sections of a title, an instruction and a format. The summary, the decisions and the actions are written for
// every format (MemoryOS TranscriptSummarizer, Nojoin meeting_notes.py), so a format lists only what it adds.
//
// The sections are copied, cut down, from:
//
//   - Hyprnote (crates/db-app/migrations/20260524000000_default_templates.sql: 1:1 Meeting, Daily Standup),
//     MIT License, Copyright (c) 2023-present Fastrepl, Inc.;
//   - Meetily (frontend/src-tauri/templates/standard_meeting.json, project_sync.json,
//     sales_marketing_client_call.json), MIT License, Copyright (c) 2024 Zackriya Solutions.
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
type Format struct {
	ID       string
	Name     string
	Sections []FormatSection
}

// FormatSection is one section a format adds to the notes.
type FormatSection struct {
	Title string
	// About says what the section holds, as Hyprnote's description and Meetily's instruction.
	About string
}

// DefaultFormat is the format of a meeting uploaded without one.
const DefaultFormat = "general"

var formats = []Format{
	{ID: "general", Name: "General", Sections: []FormatSection{
		// Meetily standard_meeting.json.
		{Title: "Discussion highlights", About: "The main topics of discussion, key arguments, and important insights."},
	}},
	{ID: "team", Name: "Team meeting", Sections: []FormatSection{
		// Meetily project_sync.json.
		{Title: "Progress summary", About: "Progress since the last meeting, in a short paragraph."},
		{Title: "Milestones & status", About: "Current milestones with their status and expected completion."},
		{Title: "Top risks & mitigations", About: "The top risks with their impact and how they are handled."},
	}},
	{ID: "one_on_one", Name: "1:1", Sections: []FormatSection{
		// Hyprnote 1:1 Meeting.
		{Title: "Updates", About: "What has been happening since last time."},
		{Title: "Wins & challenges", About: "Successes to celebrate and obstacles discussed."},
		{Title: "Goals & progress", About: "Current goals and how they are going."},
		{Title: "Feedback", About: "Feedback given both ways."},
	}},
	{ID: "standup", Name: "Stand-up", Sections: []FormatSection{
		// Hyprnote Daily Standup.
		{Title: "Yesterday", About: "What each person completed since the last stand-up."},
		{Title: "Today", About: "What each person is working on today."},
		{Title: "Blockers", About: "Obstacles, and help needed."},
	}},
	{ID: "client_call", Name: "Client call", Sections: []FormatSection{
		// Meetily sales_marketing_client_call.json.
		{Title: "Client goals & success criteria", About: "What the client wants to achieve and how success is measured."},
		{Title: "Agreed deliverables", About: "Deliverables, owners and due dates."},
		{Title: "Commercial terms discussed", About: "Pricing, SLAs, payment terms or contract items discussed."},
		{Title: "Risks & concerns", About: "Client concerns, blockers or escalation items."},
	}},
}

// Formats are the notes formats, in the order offered.
func Formats() []Format { return formats }

// FormatOf returns a format by its id.
func FormatOf(id string) (Format, bool) {
	for _, f := range formats {
		if f.ID == id {
			return f, true
		}
	}

	return Format{}, false
}
