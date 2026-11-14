package agent

import (
	"context"
	"encoding/json"
)

type EventType string

const (
	Text       EventType = "text"
	Thought    EventType = "thought"
	ToolCall   EventType = "tool"
	ToolUpdate EventType = "tool-update"
	Permission EventType = "permission"
	Withdrawn  EventType = "withdrawn"
	Plan       EventType = "plan"
	Mode       EventType = "mode"
	Commands   EventType = "commands"
	Usage      EventType = "usage"
	TurnEnded  EventType = "turn-ended"
	Ended      EventType = "ended"
	Failed     EventType = "error"
	Notice     EventType = "notice"
	Subagents  EventType = "subagents"
)

type Tool struct {
	ID     string
	Title  string
	Kind   string
	Status string
	Input  json.RawMessage
	Whole  bool
}

type Option struct {
	ID   string
	Kind string
	Name string
}

type Choice struct {
	Label       string
	Description string
}

type Question struct {
	ID      string
	Header  string
	Text    string
	Choices []Choice
}

type Request struct {
	ID        string
	Tool      Tool
	Options   []Option
	Questions []Question
}

type Subagent struct {
	ID     string
	Name   string
	Status string
}

type Command struct {
	Name        string
	Description string
}

type Spend struct {
	InputTokens  int64
	OutputTokens int64
	CostUSD      float64
	Estimate     bool
}

type Event struct {
	Type     EventType
	Text     string
	Tool     *Tool
	Request  *Request
	Mode     string
	Commands []Command
	Spend    *Spend
	Reason   string
	Agent    string
	Agents   []Subagent
}

type Answer struct {
	Allow    bool
	OptionID string
	Note     string
	Answers  map[string]string
}

type Session interface {
	Prompt(ctx context.Context, text string) error
	Cancel(ctx context.Context) error
	Answer(ctx context.Context, requestID string, a Answer) error
	Events() <-chan Event
	Close() error
}
