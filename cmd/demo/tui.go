package main

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	glamour "charm.land/glamour/v2"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/sven-victor/ez-agent/agent"
	"github.com/sven-victor/ez-agent/event"
	"github.com/sven-victor/ez-agent/run"
)

// Pane layout (top → bottom): tools | chat+input | logs | status | help
// Tools / logs / help can be toggled; chat scrolls independently of tools.

type focusPane int

const (
	focusInput focusPane = iota
	focusChat
	focusTools
	focusLogs
)

type tuiKeyMap struct {
	Quit       key.Binding
	Send       key.Binding
	Cancel     key.Binding
	FocusNext  key.Binding
	ToggleTool key.Binding
	ToggleLog  key.Binding
	ToggleHelp key.Binding
	ScrollUp   key.Binding
	ScrollDown key.Binding
	PageUp     key.Binding
	PageDown   key.Binding
}

func newTUIKeyMap() tuiKeyMap {
	return tuiKeyMap{
		Quit:       key.NewBinding(key.WithKeys("ctrl+c"), key.WithHelp("ctrl+c", "quit")),
		Send:       key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "send")),
		Cancel:     key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "cancel run")),
		FocusNext:  key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "next pane")),
		ToggleTool: key.NewBinding(key.WithKeys("ctrl+t"), key.WithHelp("ctrl+t", "toggle tools")),
		ToggleLog:  key.NewBinding(key.WithKeys("ctrl+l"), key.WithHelp("ctrl+l", "toggle logs")),
		ToggleHelp: key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "toggle help")),
		ScrollUp:   key.NewBinding(key.WithKeys("up"), key.WithHelp("↑", "scroll up")),
		ScrollDown: key.NewBinding(key.WithKeys("down"), key.WithHelp("↓", "scroll down")),
		PageUp:     key.NewBinding(key.WithKeys("pgup", "ctrl+u"), key.WithHelp("pgup", "page up")),
		PageDown:   key.NewBinding(key.WithKeys("pgdown", "ctrl+d"), key.WithHelp("pgdn", "page down")),
	}
}

func (k tuiKeyMap) helpLines() []string {
	return []string{
		"enter send · esc cancel · tab focus · ctrl+t tools · ctrl+l logs · ? help · ctrl+c quit",
		"focus chat/tools/logs: ↑/↓ scroll · pgup/pgdn page",
	}
}

type logLineMsg string

type streamPumpMsg struct {
	ev   event.Event
	ch   <-chan event.Event
	done bool
}

type tuiModel struct {
	agent     *agent.Agent
	meta      sessionMeta
	toolNames []string
	keys      tuiKeyMap

	width  int
	height int

	showTools bool
	showLogs  bool
	showHelp  bool
	focus     focusPane

	chatVP  viewport.Model
	toolsVP viewport.Model
	logsVP  viewport.Model
	input   textarea.Model

	chat       []chatEntry
	toolLines  []string
	logLines   []string
	chatFollow bool

	mdRender *glamour.TermRenderer
	mdWidth  int

	streaming   bool
	printedAsst bool
	turn        int
	usage       run.Usage
	ctxMsgs     int
	ctxTokens   int
	errText     string

	logCh    <-chan string
	statusCh <-chan statusSnapMsg

	runCancel context.CancelFunc
	runMu     sync.Mutex
	appCtx    context.Context
}

func runTUI(ctx context.Context, a *agent.Agent, toolNames []string, meta sessionMeta, logCh <-chan string) error {
	statusCh := make(chan statusSnapMsg, 64)
	attachStatusHooks(a, statusCh)
	m := newTUIModel(ctx, a, toolNames, meta, logCh, statusCh)
	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion(), tea.WithContext(ctx))
	_, err := p.Run()
	return err
}

func newTUIModel(ctx context.Context, a *agent.Agent, toolNames []string, meta sessionMeta, logCh <-chan string, statusCh <-chan statusSnapMsg) *tuiModel {
	ti := textarea.New()
	ti.Placeholder = "Message the assistant… (/quit to exit)"
	ti.Prompt = "❯ "
	ti.ShowLineNumbers = false
	ti.CharLimit = 0
	ti.SetHeight(3)
	ti.Focus()
	ti.KeyMap.InsertNewline.SetEnabled(false)

	m := &tuiModel{
		agent:      a,
		meta:       meta,
		toolNames:  toolNames,
		keys:       newTUIKeyMap(),
		showTools:  true,
		showLogs:   true,
		showHelp:   true,
		focus:      focusInput,
		chatVP:     viewport.New(0, 0),
		toolsVP:    viewport.New(0, 0),
		logsVP:     viewport.New(0, 0),
		input:      ti,
		chatFollow: true,
		logCh:      logCh,
		statusCh:   statusCh,
		appCtx:     ctx,
	}
	m.chatVP.MouseWheelEnabled = true
	m.toolsVP.MouseWheelEnabled = true
	m.logsVP.MouseWheelEnabled = true

	var intro []string
	intro = append(intro, fmt.Sprintf("ez-agent demo · %s / %s", meta.Provider, meta.Model))
	if meta.SessionID != "" {
		intro = append(intro, "session: "+meta.SessionID)
	}
	if len(toolNames) > 0 {
		intro = append(intro, "tools: "+strings.Join(toolNames, ", "))
	}
	intro = append(intro, "", "Type a message and press Enter. Tab focuses panes; ? toggles help.",
		"Assistant replies are rendered as Markdown.")
	m.chat = []chatEntry{{kind: chatPlain, text: strings.Join(intro, "\n")}}
	m.syncViewports(false)
	return m
}

func (m *tuiModel) Init() tea.Cmd {
	return tea.Batch(textarea.Blink, m.waitLog(), m.waitStatus())
}

func (m *tuiModel) waitLog() tea.Cmd {
	if m.logCh == nil {
		return nil
	}
	ch := m.logCh
	return func() tea.Msg {
		line, ok := <-ch
		if !ok {
			return nil
		}
		return logLineMsg(line)
	}
}

func (m *tuiModel) waitStatus() tea.Cmd {
	if m.statusCh == nil {
		return nil
	}
	ch := m.statusCh
	return func() tea.Msg {
		snap, ok := <-ch
		if !ok {
			return nil
		}
		return snap
	}
}

func (m *tuiModel) applyStatus(snap statusSnapMsg) {
	m.ctxMsgs = snap.msgs
	m.ctxTokens = snap.tokens
	m.turn = snap.turn
	m.usage = snap.usage
}

func (m *tuiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case streamPumpMsg:
		if msg.done {
			m.onStreamDone()
			return m, nil
		}
		m.handleStreamEvent(msg.ev)
		return m, pumpStream(msg.ch)

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.layout()
		return m, nil

	case logLineMsg:
		m.appendLog(string(msg))
		return m, m.waitLog()

	case statusSnapMsg:
		m.applyStatus(msg)
		return m, m.waitStatus()

	case tea.KeyMsg:
		return m.handleKey(msg)
	}

	return m.forwardToFocus(msg)
}

func (m *tuiModel) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, m.keys.Quit):
		m.cancelRun()
		return m, tea.Quit
	case key.Matches(msg, m.keys.ToggleTool):
		m.showTools = !m.showTools
		if !m.showTools && m.focus == focusTools {
			m.focusInputPane()
		}
		m.layout()
		return m, nil
	case key.Matches(msg, m.keys.ToggleLog):
		m.showLogs = !m.showLogs
		if !m.showLogs && m.focus == focusLogs {
			m.focusInputPane()
		}
		m.layout()
		return m, nil
	case key.Matches(msg, m.keys.ToggleHelp):
		// Allow typing "?" into the prompt when input already has text.
		if m.focus != focusInput || strings.TrimSpace(m.input.Value()) == "" {
			m.showHelp = !m.showHelp
			m.layout()
			return m, nil
		}
	case key.Matches(msg, m.keys.Cancel):
		if m.streaming {
			m.cancelRun()
		}
		return m, nil
	case key.Matches(msg, m.keys.FocusNext):
		m.cycleFocus()
		return m, nil
	}

	if m.focus != focusInput {
		m.handleScrollKeys(msg)
		return m, nil
	}

	if key.Matches(msg, m.keys.Send) {
		return m.submitInput()
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func (m *tuiModel) submitInput() (tea.Model, tea.Cmd) {
	if m.streaming {
		return m, nil
	}
	text := strings.TrimSpace(m.input.Value())
	if text == "" {
		return m, nil
	}
	m.input.Reset()
	switch {
	case text == "/quit" || text == "/exit" || text == "/q":
		return m, tea.Quit
	case text == "/help" || text == "/?":
		m.showHelp = true
		m.layout()
		return m, nil
	case text == "/tools":
		if len(m.toolNames) == 0 {
			m.appendPlain("(no tools registered)")
		} else {
			m.appendPlain("tools:\n  - " + strings.Join(m.toolNames, "\n  - "))
		}
		return m, nil
	case strings.HasPrefix(text, "/"):
		m.appendPlain(fmt.Sprintf("unknown command %q — try /help", text))
		return m, nil
	}
	return m, m.startRun(text)
}

func (m *tuiModel) forwardToFocus(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	switch m.focus {
	case focusChat:
		m.chatVP, cmd = m.chatVP.Update(msg)
		m.chatFollow = m.chatVP.AtBottom()
	case focusTools:
		m.toolsVP, cmd = m.toolsVP.Update(msg)
	case focusLogs:
		m.logsVP, cmd = m.logsVP.Update(msg)
	case focusInput:
		m.input, cmd = m.input.Update(msg)
	}
	return m, cmd
}

func (m *tuiModel) handleScrollKeys(msg tea.KeyMsg) {
	vp := m.focusedViewport()
	if vp == nil {
		return
	}
	switch {
	case key.Matches(msg, m.keys.ScrollUp):
		vp.LineUp(1)
	case key.Matches(msg, m.keys.ScrollDown):
		vp.LineDown(1)
	case key.Matches(msg, m.keys.PageUp):
		vp.ViewUp()
	case key.Matches(msg, m.keys.PageDown):
		vp.ViewDown()
	default:
		*vp, _ = vp.Update(msg)
	}
	if m.focus == focusChat {
		m.chatFollow = m.chatVP.AtBottom()
	}
}

func (m *tuiModel) focusedViewport() *viewport.Model {
	switch m.focus {
	case focusChat:
		return &m.chatVP
	case focusTools:
		if m.showTools {
			return &m.toolsVP
		}
	case focusLogs:
		if m.showLogs {
			return &m.logsVP
		}
	}
	return nil
}

func (m *tuiModel) focusInputPane() {
	m.focus = focusInput
	m.input.Focus()
}

func (m *tuiModel) cycleFocus() {
	order := []focusPane{focusInput, focusChat}
	if m.showTools {
		order = append(order, focusTools)
	}
	if m.showLogs {
		order = append(order, focusLogs)
	}
	idx := 0
	for i, p := range order {
		if p == m.focus {
			idx = i
			break
		}
	}
	m.focus = order[(idx+1)%len(order)]
	if m.focus == focusInput {
		m.input.Focus()
	} else {
		m.input.Blur()
	}
}

func (m *tuiModel) startRun(text string) tea.Cmd {
	m.errText = ""
	m.appendUser(text)
	m.streaming = true
	m.printedAsst = false
	m.turn = 0
	m.chatFollow = true
	m.syncViewports(true)

	runCtx, cancel := context.WithCancel(m.appCtx)
	m.setRunCancel(cancel)

	ch := m.agent.RunStream(runCtx, run.Input{Text: text})
	return pumpStream(ch)
}

func pumpStream(ch <-chan event.Event) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-ch
		if !ok {
			return streamPumpMsg{done: true}
		}
		return streamPumpMsg{ev: ev, ch: ch}
	}
}

func (m *tuiModel) onStreamDone() {
	m.streaming = false
	m.printedAsst = false
	m.setRunCancel(nil)
	// Finalize markdown cache for the last assistant bubble.
	if n := len(m.chat); n > 0 && m.chat[n-1].kind == chatAssistant {
		m.chat[n-1].rendered = ""
		m.chat[n-1].renderW = 0
	}
	m.chatFollow = true
	m.syncViewports(true)
	m.focusInputPane()
}

func (m *tuiModel) handleStreamEvent(ev event.Event) {
	switch e := ev.(type) {
	case event.ModelDelta:
		if !m.printedAsst {
			m.beginAssistant(e.TextDelta)
			m.printedAsst = true
		} else {
			m.appendAssistantDelta(e.TextDelta)
		}
	case event.ToolStarted:
		m.printedAsst = false
		m.appendTool(fmt.Sprintf("→ %s(%s)", e.Call.Name, compactJSON(e.Call.Input)))
	case event.ToolFinished:
		status := "ok"
		if !e.Result.OK {
			status = "err"
		}
		m.appendTool(fmt.Sprintf("← %s [%s] %s", e.Call.Name, status, truncate(partsText(e.Result.Content), 200)))
	case event.RunError:
		m.errText = e.Err.Error()
		m.appendError(e.Err.Error())
	case event.Cancelled:
		m.appendPlain("(canceled)")
	}
	m.syncViewports(m.chatFollow)
}

func (m *tuiModel) setRunCancel(fn context.CancelFunc) {
	m.runMu.Lock()
	defer m.runMu.Unlock()
	m.runCancel = fn
}

func (m *tuiModel) cancelRun() {
	m.runMu.Lock()
	fn := m.runCancel
	m.runMu.Unlock()
	if fn != nil {
		fn()
	}
}

func (m *tuiModel) appendPlain(text string) {
	m.chat = append(m.chat, chatEntry{kind: chatPlain, text: text})
	m.syncViewports(m.chatFollow)
}

func (m *tuiModel) appendUser(text string) {
	m.chat = append(m.chat, chatEntry{kind: chatUser, text: text})
}

func (m *tuiModel) appendError(text string) {
	m.chat = append(m.chat, chatEntry{kind: chatError, text: text})
}

func (m *tuiModel) beginAssistant(text string) {
	m.chat = append(m.chat, chatEntry{kind: chatAssistant, text: text})
}

func (m *tuiModel) appendAssistantDelta(delta string) {
	if n := len(m.chat); n > 0 && m.chat[n-1].kind == chatAssistant {
		m.chat[n-1].text += delta
		m.chat[n-1].rendered = ""
		return
	}
	m.beginAssistant(delta)
}

func (m *tuiModel) appendTool(line string) {
	ts := time.Now().Format("15:04:05")
	m.toolLines = append(m.toolLines, ts+" "+line)
	m.toolsVP.SetContent(strings.Join(m.toolLines, "\n"))
	m.toolsVP.GotoBottom()
}

func (m *tuiModel) appendLog(line string) {
	m.logLines = append(m.logLines, line)
	const maxLogs = 500
	if len(m.logLines) > maxLogs {
		m.logLines = m.logLines[len(m.logLines)-maxLogs:]
	}
	m.logsVP.SetContent(strings.Join(m.logLines, "\n"))
	m.logsVP.GotoBottom()
}

func (m *tuiModel) syncViewports(followChat bool) {
	m.chatVP.SetContent(m.renderChatContent())
	if followChat {
		m.chatVP.GotoBottom()
	}
	m.toolsVP.SetContent(strings.Join(m.toolLines, "\n"))
	m.logsVP.SetContent(strings.Join(m.logLines, "\n"))
}

func (m *tuiModel) layout() {
	if m.width <= 0 || m.height <= 0 {
		return
	}
	const (
		statusH    = 1
		inputH     = 3
		chatTitleH = 1
		minChatVP  = 3
	)
	helpH := 0
	if m.showHelp {
		helpH = 1 + len(m.keys.helpLines()) // title + lines
	}
	remain := m.height - statusH - helpH - inputH - chatTitleH
	if remain < minChatVP {
		remain = minChatVP
	}

	toolsH, logsH := 0, 0
	if m.showTools {
		toolsH = max(3, remain/5) // includes title row
	}
	if m.showLogs {
		logsH = max(3, remain/5)
	}
	chatVP := remain - toolsH - logsH
	if chatVP < minChatVP {
		need := minChatVP - chatVP
		if logsH > 3 {
			cut := min(need, logsH-3)
			logsH -= cut
			need -= cut
		}
		if need > 0 && toolsH > 3 {
			toolsH -= min(need, toolsH-3)
		}
		chatVP = remain - toolsH - logsH
		if chatVP < 1 {
			chatVP = 1
		}
	}

	innerW := max(1, m.width-2)
	m.toolsVP.Width = innerW
	m.toolsVP.Height = max(1, toolsH-1)
	m.chatVP.Width = innerW
	m.chatVP.Height = max(1, chatVP)
	m.logsVP.Width = innerW
	m.logsVP.Height = max(1, logsH-1)
	m.input.SetWidth(m.width - 2)
	m.input.SetHeight(inputH)
	m.syncViewports(m.chatFollow)
}

func (m *tuiModel) View() string {
	if m.width == 0 {
		return "loading…"
	}

	var b strings.Builder
	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12"))
	dim := lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	focusMark := func(p focusPane, label string) string {
		if m.focus == p {
			return titleStyle.Render("▸ " + label)
		}
		return dim.Render("  " + label)
	}

	if m.showTools {
		b.WriteString(focusMark(focusTools, "Tools"))
		b.WriteString(dim.Render("  (ctrl+t)"))
		b.WriteByte('\n')
		b.WriteString(m.toolsVP.View())
		b.WriteByte('\n')
	}

	b.WriteString(focusMark(focusChat, "Chat"))
	if m.streaming {
		b.WriteString(dim.Render("  · streaming…"))
	}
	b.WriteByte('\n')
	b.WriteString(m.chatVP.View())
	b.WriteByte('\n')
	b.WriteString(m.input.View())
	b.WriteByte('\n')

	if m.showLogs {
		b.WriteString(focusMark(focusLogs, "Logs"))
		b.WriteString(dim.Render("  (ctrl+l)"))
		b.WriteByte('\n')
		b.WriteString(m.logsVP.View())
		b.WriteByte('\n')
	}

	b.WriteString(m.statusLine())
	if m.showHelp {
		b.WriteByte('\n')
		b.WriteString(titleStyle.Render("Help"))
		b.WriteString(dim.Render("  (?)"))
		b.WriteByte('\n')
		for _, line := range m.keys.helpLines() {
			b.WriteString(dim.Render(line))
			b.WriteByte('\n')
		}
	}
	return b.String()
}

func (m *tuiModel) statusLine() string {
	style := lipgloss.NewStyle().
		Background(lipgloss.Color("236")).
		Foreground(lipgloss.Color("252")).
		Width(m.width)

	turns := fmt.Sprintf("%d", m.turn)
	if m.meta.MaxTurns > 0 {
		turns = fmt.Sprintf("%d/%d", m.turn, m.meta.MaxTurns)
	} else {
		turns += "/∞"
	}

	parts := []string{
		fmt.Sprintf("ctx %d msgs ~%d tok", m.ctxMsgs, m.ctxTokens),
		"turn " + turns,
		fmt.Sprintf("tok in=%d out=%d total=%d", m.usage.InputTokens, m.usage.OutputTokens, m.usage.TotalTokens),
		"mcp " + m.meta.MCP,
		m.meta.Provider + "/" + m.meta.Model,
	}
	if m.errText != "" {
		parts = append(parts, "err:"+truncate(m.errText, 40))
	}
	return style.Render(strings.Join(parts, " │ "))
}
