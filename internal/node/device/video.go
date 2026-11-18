package device

import (
	"context"
	"errors"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/kl3inIT/hypomnema/internal/shared/devicetool"
	"github.com/kl3inIT/hypomnema/internal/shared/nodeproto"
)

// The video tools on the node (issue #817): the control plane passes an
// agent's video_start, video_say, video_stop and video_status, and its own
// video_pause, video_resume and video_end, as it passes a phone tool; the
// node films (take.go) and answers with the take as it holds it, which the
// control plane keeps.

// versionName is what `dumpsys package` says of an app's version.
var versionName = regexp.MustCompile(`versionName=(\S+)`)

// video carries out a video tool for an agent.
func (m *Manager) video(ctx context.Context, agent string, c devicetool.Call) nodeproto.DeviceAnswer {
	var (
		take devicetool.Take
		err  error
	)

	switch c.Tool {
	case devicetool.VideoStart:
		if answer, ready := m.ready(ctx); !ready {
			return answer
		}

		m.Touch()

		take, err = m.StartTake(ctx, agent, c.Take, c.Title, c.RequestID)
	case devicetool.VideoSay:
		var tap func() (*devicetool.TakeTap, error)
		if c.Target != nil {
			tap = func() (*devicetool.TakeTap, error) { return m.tapPoint(ctx, agent, *c.Target) }
		}

		take, err = m.SayTake(c.Take, agent, c.Text, c.Chapter, tap)
	case devicetool.VideoStop:
		take, err = m.stopOwn(c.Take, agent)
	case devicetool.VideoStatus:
		take, err = m.TakeStatus(c.Take)
	case devicetool.VideoPause:
		take, err = m.PauseTake(c.Take)
	case devicetool.VideoResume:
		take, err = m.ResumeTake(c.Take)
	case devicetool.VideoEnd:
		take, err = m.StopTake(c.Take, c.EndedBy, "")
	default:
		return failed("no such video tool: " + c.Tool)
	}

	if err != nil {
		return failed(err.Error())
	}

	said := devicetool.Describe(take)
	if c.Tool == devicetool.VideoSay && len(take.Steps) > 0 {
		said = "Said at " + devicetool.Clock(lastSaid(take, c.Text)) + " of take " + take.ID + "."
	}

	return nodeproto.DeviceAnswer{Content: []nodeproto.DeviceContent{{Type: "text", Text: said}}, Take: &take}
}

// lastSaid is when the line was said last.
func lastSaid(t devicetool.Take, text string) int64 {
	var at int64

	for _, s := range t.Steps {
		if s.Text == text {
			at = max(at, s.AtMs)
		}
	}

	return at
}

// stopOwn stops an agent's own take.
func (m *Manager) stopOwn(id, agent string) (devicetool.Take, error) {
	take, err := m.TakeStatus(id)
	if err != nil {
		return devicetool.Take{}, err
	}

	if take.Agent != agent {
		return devicetool.Take{}, ErrNoTake
	}

	return m.StopTake(id, devicetool.EndedStop, "")
}

// tapPoint resolves where an agent is about to tap, as device_tap resolves a
// point: a box of its last screen, or the coordinates given.
func (m *Manager) tapPoint(ctx context.Context, agent string, p devicetool.Point) (*devicetool.TakeTap, error) {
	m.agents.mu.Lock()
	defer m.agents.mu.Unlock()

	ctx, cancel := context.WithTimeout(ctx, toolWithin)
	defer cancel()

	x, y, why := m.point(ctx, agent, p)
	if why != "" {
		return nil, errors.New(why)
	}

	px, errX := strconv.Atoi(x)
	py, errY := strconv.Atoi(y)

	if errX != nil || errY != nil {
		return nil, errors.New("the point could not be read")
	}

	return &devicetool.TakeTap{X: px, Y: py}, nil
}

// launchedFor records the app an agent opened as its take's app, with its
// version as the phone's package manager says it.
func (m *Manager) launchedFor(ctx context.Context, agent, pkg string) {
	if !m.filmingFor(agent) {
		return
	}

	version := ""

	out, err := m.adb(ctx, 10*time.Second, "-s", serial, "shell", "dumpsys", "package", pkg)
	if err == nil && out.Code == 0 {
		if match := versionName.FindStringSubmatch(out.Stdout); match != nil {
			version = strings.TrimSpace(match[1])
		}
	}

	m.launchedApp(agent, pkg, version)
}

// filmingFor says whether a take of agent's is being filmed.
func (m *Manager) filmingFor(agent string) bool {
	m.film.mu.Lock()
	defer m.film.mu.Unlock()

	return m.film.cur != nil && m.film.cur.meta.Agent == agent
}
