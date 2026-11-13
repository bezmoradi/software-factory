package codex

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
)

const (
	maxLine   = 8 << 20
	readChunk = 64 << 10
	maxIDLen  = 256
)

const (
	methodInitialize    = "initialize"
	methodInitialized   = "initialized"
	methodAccountRead   = "account/read"
	methodThreadStart   = "thread/start"
	methodTurnStart     = "turn/start"
	methodTurnInterrupt = "turn/interrupt"

	methodCommandApproval   = "item/commandExecution/requestApproval"
	methodFileApproval      = "item/fileChange/requestApproval"
	methodElicitation       = "mcpServer/elicitation/request"
	methodPermissions       = "item/permissions/requestApproval"
	methodUserInput         = "item/tool/requestUserInput"
	methodDynamicTool       = "item/tool/call"
	methodTokenRefresh      = "account/chatgptAuthTokens/refresh"
	methodAttestation       = "attestation/generate"
	methodLegacyExec        = "execCommandApproval"
	methodLegacyPatch       = "applyPatchApproval"
	methodAgentDelta        = "item/agentMessage/delta"
	methodReasoningSummary  = "item/reasoning/summaryTextDelta"
	methodReasoningText     = "item/reasoning/textDelta"
	methodItemStarted       = "item/started"
	methodItemCompleted     = "item/completed"
	methodPatchUpdated      = "item/fileChange/patchUpdated"
	methodTurnStarted       = "turn/started"
	methodTurnCompleted     = "turn/completed"
	methodPlanUpdated       = "turn/plan/updated"
	methodTokenUsage        = "thread/tokenUsage/updated"
	methodSettingsUpdated   = "thread/settings/updated"
	methodRequestResolved   = "serverRequest/resolved"
	methodError             = "error"
	methodThreadClosed      = "thread/closed"
	methodThreadStatus      = "thread/status/changed"
	methodCommandOutput     = "item/commandExecution/outputDelta"
	methodFileOutput        = "item/fileChange/outputDelta"
	methodSummaryPartAdded  = "item/reasoning/summaryPartAdded"
	methodThreadStarted     = "thread/started"
	methodMcpProgress       = "item/mcpToolCall/progress"
	methodTurnDiff          = "turn/diff/updated"
	methodAccountUpdated    = "account/updated"
	methodRateLimits        = "account/rateLimits/updated"
	methodMcpStartup        = "mcpServer/startupStatus/updated"
	methodWarning           = "warning"
	methodConfigWarning     = "configWarning"
	methodDeprecationNotice = "deprecationNotice"
)

const (
	policyOnRequest   = "on-request"
	reviewerUser      = "user"
	sandboxNone       = "danger-full-access"
	decisionAccept    = "accept"
	decisionDecline   = "decline"
	decisionCancel    = "cancel"
	codeNotSupported  = -32601
	notSupportedText  = "not supported by this client"
	legacyRejection   = "this client answers only v2 approval requests"
	elicitToolCall    = "mcp_tool_call"
	elicitModeForm    = "form"
	commandKind       = "command"
	errorUnauthorized = "unauthorized"
)

var quietNotifications = map[string]bool{
	methodCommandOutput:     true,
	methodFileOutput:        true,
	methodSummaryPartAdded:  true,
	methodThreadStarted:     true,
	methodMcpProgress:       true,
	methodTurnDiff:          true,
	methodAccountUpdated:    true,
	methodRateLimits:        true,
	methodMcpStartup:        true,
	methodWarning:           true,
	methodConfigWarning:     true,
	methodDeprecationNotice: true,
}

var optOut = []string{methodCommandOutput, methodFileOutput}

type envelope struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	Result json.RawMessage `json:"result"`
	Error  *rpcError       `json:"error"`
}

type rpcError struct {
	Code    int64  `json:"code"`
	Message string `json:"message"`
}

type outRequest struct {
	ID     int64  `json:"id"`
	Method string `json:"method"`
	Params any    `json:"params"`
}

type outNotification struct {
	Method string `json:"method"`
}

type outResult struct {
	ID     json.RawMessage `json:"id"`
	Result any             `json:"result"`
}

type outError struct {
	ID    json.RawMessage `json:"id"`
	Error rpcError        `json:"error"`
}

type clientInfo struct {
	Name    string `json:"name"`
	Title   string `json:"title"`
	Version string `json:"version"`
}

type capabilities struct {
	ExperimentalAPI bool     `json:"experimentalApi"`
	OptOut          []string `json:"optOutNotificationMethods"`
}

type initializeParams struct {
	ClientInfo   clientInfo   `json:"clientInfo"`
	Capabilities capabilities `json:"capabilities"`
}

type threadStartParams struct {
	Cwd               string                     `json:"cwd"`
	ApprovalPolicy    string                     `json:"approvalPolicy"`
	ApprovalsReviewer string                     `json:"approvalsReviewer"`
	Sandbox           string                     `json:"sandbox"`
	Config            map[string]json.RawMessage `json:"config,omitempty"`
}

type textInput struct {
	Type         string   `json:"type"`
	Text         string   `json:"text"`
	TextElements []string `json:"text_elements"`
}

type localImage struct {
	Type string `json:"type"`
	Path string `json:"path"`
}

type turnStartParams struct {
	ThreadID          string `json:"threadId"`
	Input             []any  `json:"input"`
	ApprovalPolicy    string `json:"approvalPolicy"`
	ApprovalsReviewer string `json:"approvalsReviewer"`
}

type turnInterruptParams struct {
	ThreadID string `json:"threadId"`
	TurnID   string `json:"turnId"`
}

type decisionReply struct {
	Decision string `json:"decision"`
}

type elicitationReply struct {
	Action  string    `json:"action"`
	Content *struct{} `json:"content"`
}

type permissionsReply struct {
	Permissions struct{} `json:"permissions"`
	Scope       string   `json:"scope"`
}

type legacyDenied struct {
	Rejection string `json:"rejection"`
}

type legacyReply struct {
	Decision struct {
		Denied legacyDenied `json:"denied"`
	} `json:"decision"`
}

type userInputReply struct {
	Answers map[string]struct{} `json:"answers"`
}

var errLineTooLong = errors.New("codex: line over the limit")

type lineReader struct {
	r   *bufio.Reader
	buf []byte
}

func newLineReader(r io.Reader) *lineReader {
	return &lineReader{r: bufio.NewReaderSize(r, readChunk)}
}

func (l *lineReader) next() ([]byte, error) {
	l.buf = l.buf[:0]
	over := false
	for {
		frag, err := l.r.ReadSlice('\n')
		if !over {
			if len(l.buf)+len(frag) > maxLine+1 {
				over = true
				l.buf = l.buf[:0]
			} else {
				l.buf = append(l.buf, frag...)
			}
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if over {
			return nil, errLineTooLong
		}
		if err == nil || (errors.Is(err, io.EOF) && len(l.buf) > 0) {
			return bytes.TrimRight(l.buf, "\r\n"), nil
		}
		return nil, err
	}
}

func validID(raw json.RawMessage) (json.RawMessage, bool) {
	if len(raw) == 0 || len(raw) > maxIDLen {
		return nil, false
	}
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return nil, false
	}
	switch v.(type) {
	case string, float64:
	default:
		return nil, false
	}
	var out bytes.Buffer
	if json.Compact(&out, raw) != nil {
		return nil, false
	}
	return out.Bytes(), true
}

func isNull(raw json.RawMessage) bool {
	t := bytes.TrimSpace(raw)
	return len(t) == 0 || bytes.Equal(t, []byte("null"))
}

func isObject(raw json.RawMessage) bool {
	var m map[string]json.RawMessage
	return json.Unmarshal(raw, &m) == nil && m != nil
}
