package main

import (
	"strings"

	glamour "charm.land/glamour/v2"
	"github.com/charmbracelet/lipgloss"
)

type chatKind int

const (
	chatPlain chatKind = iota
	chatUser
	chatAssistant // markdown body
	chatError
)

type chatEntry struct {
	kind     chatKind
	text     string
	rendered string // cached ANSI for assistant at renderedAtW
	renderW  int
}

func (m *tuiModel) ensureMDRenderer(width int) {
	if width < 20 {
		width = 20
	}
	if m.mdRender != nil && m.mdWidth == width {
		return
	}
	r, err := glamour.NewTermRenderer(
		// glamour.WithStandardStyle("light"),
		glamour.WithWordWrap(width),
		glamour.WithPreservedNewLines(),
	)
	if err != nil {
		m.mdRender = nil
		m.mdWidth = width
		return
	}
	m.mdRender = r
	m.mdWidth = width
	// Width changed → drop assistant caches.
	for i := range m.chat {
		if m.chat[i].kind == chatAssistant {
			m.chat[i].rendered = ""
			m.chat[i].renderW = 0
		}
	}
}

func (m *tuiModel) renderAssistantMarkdown(raw string, width int) string {
	m.ensureMDRenderer(width)
	body := strings.TrimRight(raw, "\n")
	if body == "" {
		return ""
	}
	if m.mdRender == nil {
		return body
	}
	out, err := m.mdRender.Render(body)
	if err != nil {
		return body
	}
	return strings.TrimRight(out, "\n")
}

func (m *tuiModel) formatChatEntry(e *chatEntry, width int, streaming bool) string {
	label := lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Bold(true)
	switch e.kind {
	case chatUser:
		you := lipgloss.NewStyle().Foreground(lipgloss.Color("10")).Bold(true)
		return you.Render("you") + "  " + e.text
	case chatAssistant:
		head := label.Render("assistant")
		if streaming {
			head += lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Render(" …")
		}
		mdW := width - 2
		if mdW < 20 {
			mdW = 20
		}
		var body string
		if !streaming && e.rendered != "" && e.renderW == mdW {
			body = e.rendered
		} else {
			body = m.renderAssistantMarkdown(e.text, mdW)
			if !streaming {
				e.rendered = body
				e.renderW = mdW
			}
		}
		if body == "" {
			return head
		}
		return head + "\n" + body
	case chatError:
		errStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
		return errStyle.Render("error: " + e.text)
	default:
		return e.text
	}
}

func (m *tuiModel) renderChatContent() string {
	width := m.chatVP.Width
	if width <= 0 {
		width = max(1, m.width-2)
	}
	var b strings.Builder
	for i := range m.chat {
		e := &m.chat[i]
		streaming := m.streaming && e.kind == chatAssistant && i == len(m.chat)-1
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(m.formatChatEntry(e, width, streaming))
		if e.kind == chatAssistant || e.kind == chatUser {
			b.WriteByte('\n') // spacer after turns
		}
	}
	return b.String()
}
