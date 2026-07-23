package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/sven-victor/ez-agent/logger"
	"github.com/sven-victor/ez-agent/message"
	"github.com/sven-victor/ez-agent/model"
	"github.com/sven-victor/ez-agent/tool"
)

const (
	defaultSegmentedMaxTurns = 20
	toolGetMessages          = "get_messages"
	toolSaveSummary          = "save_summary"
)

// SegmentedSummarizer condenses history via a tool-driven mini loop
// (get_messages / save_summary), similar to ez-console segmented summarization.
// It does not nest agent.Run; it only calls Provider.Complete with summary tools.
type SegmentedSummarizer struct {
	Provider  model.Provider
	MaxTurns  int // default 20
	MaxTokens int // optional generation cap for each Complete
	System    string
	// OmitLatestIntent skips injecting the latest user intent into the kickoff prompt.
	OmitLatestIntent bool
	// Logger receives summarizer diagnostics; nil is a no-op.
	Logger logger.Logger
}

// Summarize runs segmented summarization and returns a single user summary message.
func (s *SegmentedSummarizer) Summarize(ctx context.Context, msgs []message.Message) (message.Message, error) {
	if s == nil || s.Provider == nil {
		return message.Message{}, fmt.Errorf("segmented summarizer: provider is required")
	}
	log := policyLog(s.Logger)
	if len(msgs) == 0 {
		return message.Message{}, fmt.Errorf("segmented summarizer: no messages")
	}
	if !s.Provider.Caps().Tools {
		return message.Message{}, fmt.Errorf("segmented summarizer: provider must support tools")
	}

	store := newSummaryMessageStore(msgs)
	regs := summaryToolRegistrations(store)
	reg := tool.NewRegistry(regs...)
	exec := &tool.Concurrent{Registry: reg}

	intent := ""
	if !s.OmitLatestIntent {
		intent = lastUserText(msgs, defaultIntentBytes)
	}
	system := s.buildSystem(store)
	userPrompt := "Begin segmented summarization. Start by fetching the first segment of messages using get_messages."
	if intent != "" {
		userPrompt += "\n\nWhen condensing, keep content aligned with the latest user intent. Latest user message (excerpt):\n" + intent
	}

	hist := []message.Message{message.NewUserText(userPrompt)}
	maxTurns := s.MaxTurns
	if maxTurns <= 0 {
		maxTurns = defaultSegmentedMaxTurns
	}

	log.Info("segmented summarizer started", "messages", len(msgs), "max_turns", maxTurns)
	for turn := 0; turn < maxTurns; turn++ {
		req := model.Request{
			Messages:   append([]message.Message(nil), hist...),
			System:     model.System{Text: system},
			Tools:      reg.Specs(),
			ToolChoice: tool.Choice{Type: "auto"},
			MaxTokens:  s.MaxTokens,
		}
		res, err := s.Provider.Complete(ctx, req)
		if err != nil {
			log.Error("segmented summarizer model failed", "turn", turn, "err", err)
			return message.Message{}, fmt.Errorf("segmented summarizer: %w", err)
		}
		hist = append(hist, res.Message)

		uses := message.ToolUses(res.Message)
		if len(uses) == 0 {
			if store.finished {
				break
			}
			err := fmt.Errorf("segmented summarizer: model stopped without save_summary")
			log.Error("segmented summarizer incomplete", "turn", turn, "err", err)
			return message.Message{}, err
		}

		calls := make([]tool.Call, len(uses))
		for i, u := range uses {
			calls[i] = tool.Call{ID: u.ID, Name: u.Name, Input: u.Input}
		}
		log.Debug("segmented summarizer tools", "turn", turn, "count", len(calls))
		results, err := exec.Execute(ctx, calls)
		if err != nil {
			log.Error("segmented summarizer tool failed", "turn", turn, "err", err)
			return message.Message{}, err
		}
		tr := make([]message.ToolResult, len(results))
		for i, r := range results {
			tr[i] = message.ToolResult{
				ToolUseID: r.ToolUseID,
				OK:        r.OK,
				Parts:     r.Content,
			}
			if !r.OK && r.Err != nil && len(r.Content) == 0 {
				tr[i].Parts = []message.Part{message.Text{Text: r.Err.Error()}}
			}
		}
		hist = append(hist, message.NewToolResultMessage(tr...))
		if store.finished {
			log.Info("segmented summarizer finished", "turns", turn+1, "summary_bytes", len(store.summary))
			break
		}
	}

	if !store.finished || strings.TrimSpace(store.summary) == "" {
		err := fmt.Errorf("segmented summarizer: save_summary was not called")
		log.Error("segmented summarizer failed", "err", err)
		return message.Message{}, err
	}
	return summaryMessage("[Conversation summary]\n" + store.summary), nil
}

func (s *SegmentedSummarizer) buildSystem(store *summaryMessageStore) string {
	var b strings.Builder
	if s.System != "" {
		b.WriteString(s.System)
		b.WriteString("\n\n")
	}
	b.WriteString("You are a conversation summarizer. The conversation must be condensed through segmented summarization.\n\n")
	b.WriteString("## Message Overview\n\n")
	b.WriteString("| # | ID | Role | Length |\n")
	b.WriteString("|---|-----|------|--------|\n")
	total := 0
	for i, e := range store.entries {
		fmt.Fprintf(&b, "| %d | %s | %s | %d |\n", i, e.ID, e.Role, e.Length)
		total += e.Length
	}
	fmt.Fprintf(&b, "\n**Total messages:** %d | **Total content length:** %d chars\n\n", len(store.entries), total)
	b.WriteString("## Instructions\n\n")
	b.WriteString("1. Use `get_messages` with message IDs from the table to read content. You may request multiple IDs in one call.\n")
	b.WriteString("2. For very long messages, read them in chunks with `start`/`end` byte offsets.\n")
	b.WriteString("3. After reading each batch, keep a running summary.\n")
	b.WriteString("4. Tool-call / tool-result pairs should be condensed into a brief description of what was done and the outcome.\n")
	b.WriteString("5. Once all messages are processed, call `save_summary` with the final condensed text (or message array).\n")
	b.WriteString("6. The result must be significantly shorter than the original.\n")
	return b.String()
}

type summaryEntry struct {
	ID      string
	Role    string
	Content string
	Length  int
}

type summaryMessageStore struct {
	entries  []summaryEntry
	byID     map[string]*summaryEntry
	summary  string
	finished bool
}

func newSummaryMessageStore(msgs []message.Message) *summaryMessageStore {
	s := &summaryMessageStore{
		entries: make([]summaryEntry, len(msgs)),
		byID:    make(map[string]*summaryEntry, len(msgs)),
	}
	for i, m := range msgs {
		id := fmt.Sprintf("m%02d", i)
		content := flattenMessageText(m)
		s.entries[i] = summaryEntry{
			ID:      id,
			Role:    string(m.Role),
			Content: content,
			Length:  len(content),
		}
		s.byID[id] = &s.entries[i]
	}
	return s
}

func summaryToolRegistrations(store *summaryMessageStore) []tool.Registration {
	return []tool.Registration{
		{
			Spec: tool.Spec{
				Name:        toolGetMessages,
				Description: "Read content from one or more messages by ID, with optional start/end byte offsets.",
				InputSchema: json.RawMessage(`{
					"type":"object",
					"properties":{
						"message_ids":{
							"type":"array",
							"items":{
								"type":"object",
								"properties":{
									"id":{"type":"string"},
									"start":{"type":"integer"},
									"end":{"type":"integer"}
								},
								"required":["id"]
							}
						}
					},
					"required":["message_ids"]
				}`),
			},
			Handler: func(ctx context.Context, call tool.Call) (tool.Result, error) {
				out, err := store.getMessages(call.Input)
				if err != nil {
					return tool.Result{
						ToolUseID: call.ID,
						OK:        false,
						Content:   []message.Part{message.Text{Text: err.Error()}},
						Err:       err,
					}, nil
				}
				return tool.Result{
					ToolUseID: call.ID,
					OK:        true,
					Content:   []message.Part{message.Text{Text: out}},
				}, nil
			},
		},
		{
			Spec: tool.Spec{
				Name:        toolSaveSummary,
				Description: "Finalize the condensed conversation summary. Call once when done.",
				InputSchema: json.RawMessage(`{
					"type":"object",
					"properties":{
						"summary":{"type":"string","description":"Final condensed summary text"},
						"messages":{
							"type":"array",
							"items":{
								"type":"object",
								"properties":{
									"role":{"type":"string"},
									"content":{"type":"string"}
								},
								"required":["content"]
							}
						}
					}
				}`),
			},
			Handler: func(ctx context.Context, call tool.Call) (tool.Result, error) {
				if err := store.saveSummary(call.Input); err != nil {
					return tool.Result{
						ToolUseID: call.ID,
						OK:        false,
						Content:   []message.Part{message.Text{Text: err.Error()}},
						Err:       err,
					}, nil
				}
				return tool.Result{
					ToolUseID: call.ID,
					OK:        true,
					Content:   []message.Part{message.Text{Text: "Summary saved successfully."}},
				}, nil
			},
		},
	}
}

func (s *summaryMessageStore) getMessages(raw json.RawMessage) (string, error) {
	var params struct {
		MessageIDs []struct {
			ID    string `json:"id"`
			Start *int   `json:"start"`
			End   *int   `json:"end"`
		} `json:"message_ids"`
	}
	if err := json.Unmarshal(raw, &params); err != nil {
		return "", fmt.Errorf("invalid get_messages args: %w", err)
	}
	if len(params.MessageIDs) == 0 {
		return "", fmt.Errorf("message_ids must not be empty")
	}
	type readResult struct {
		ID      string `json:"id"`
		Role    string `json:"role"`
		Content string `json:"content"`
		Start   int    `json:"start"`
		End     int    `json:"end"`
		Total   int    `json:"total"`
	}
	out := make([]readResult, 0, len(params.MessageIDs))
	for _, req := range params.MessageIDs {
		e, ok := s.byID[req.ID]
		if !ok {
			return "", fmt.Errorf("message ID %q not found", req.ID)
		}
		start := 0
		if req.Start != nil {
			start = *req.Start
		}
		end := e.Length
		if req.End != nil {
			end = *req.End
		}
		if start < 0 {
			start = 0
		}
		if end > e.Length {
			end = e.Length
		}
		if start > end {
			start = end
		}
		content := ""
		if e.Length > 0 {
			content = e.Content[start:end]
		}
		out = append(out, readResult{
			ID: e.ID, Role: e.Role, Content: content,
			Start: start, End: end, Total: e.Length,
		})
	}
	b, err := json.Marshal(out)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func (s *summaryMessageStore) saveSummary(raw json.RawMessage) error {
	var params struct {
		Summary  string `json:"summary"`
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(raw, &params); err != nil {
		return fmt.Errorf("invalid save_summary args: %w", err)
	}
	text := strings.TrimSpace(params.Summary)
	if text == "" && len(params.Messages) > 0 {
		var b strings.Builder
		for i, m := range params.Messages {
			if i > 0 {
				b.WriteString("\n\n")
			}
			c := strings.TrimSpace(m.Content)
			if c == "" {
				continue
			}
			if m.Role != "" {
				fmt.Fprintf(&b, "[%s] %s", m.Role, c)
			} else {
				b.WriteString(c)
			}
		}
		text = strings.TrimSpace(b.String())
	}
	if text == "" {
		return fmt.Errorf("summary or messages is required")
	}
	s.summary = text
	s.finished = true
	return nil
}

var _ Summarizer = (*SegmentedSummarizer)(nil)
