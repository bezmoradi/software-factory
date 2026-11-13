package agenthost

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	"github.com/bezmoradi/software-factory/internal/core/session"
)

type signInJSON struct {
	commandJSON
	Kind string `json:"kind"`
}

type signInReply struct {
	Method   string `json:"method"`
	Endpoint string `json:"endpoint,omitempty"`
}

type claudeStatus struct {
	LoggedIn    bool   `json:"loggedIn"`
	AuthMethod  string `json:"authMethod"`
	APIProvider string `json:"apiProvider"`
}

const firstPartyHost = "api.anthropic.com"

func ClaudeSignIn(jsonOut, textOut string) session.SignIn {
	var st claudeStatus
	if json.Unmarshal([]byte(lastObject(jsonOut)), &st) != nil {
		return session.SignIn{Method: session.SignInUnknown}
	}
	endpoint := baseURLHost(textOut)
	custom := endpoint != "" && endpoint != firstPartyHost
	if !custom {
		endpoint = ""
	}
	out := session.SignIn{Endpoint: endpoint}
	switch {
	case !st.LoggedIn || st.AuthMethod == "none":
		out.Method = session.SignInNone
	case st.APIProvider == "bedrock" || st.APIProvider == "mantle":
		out.Method = session.SignInBedrock
	case st.APIProvider == "vertex":
		out.Method = session.SignInVertex
	case st.APIProvider == "gateway":
		out.Method = session.SignInGateway
	case st.APIProvider != "firstParty":
		out.Method = session.SignInUnknown
	case st.AuthMethod == "claude.ai":
		out.Method = session.SignInSubscription
	case (st.AuthMethod == "api_key" || st.AuthMethod == "api_key_helper" || st.AuthMethod == "oauth_token") && custom:
		out.Method = session.SignInGateway
	case st.AuthMethod == "api_key" || st.AuthMethod == "api_key_helper":
		out.Method = session.SignInAPIKey
	default:
		out.Method = session.SignInUnknown
	}
	return out
}

func lastObject(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.Index(s, "{"); i >= 0 {
		return s[i:]
	}
	return ""
}

func baseURLHost(text string) string {
	for line := range strings.SplitSeq(text, "\n") {
		v, ok := strings.CutPrefix(strings.TrimSpace(line), "Anthropic base URL:")
		if !ok {
			continue
		}
		u, err := url.Parse(strings.TrimSpace(v))
		if err != nil || u.Host == "" {
			return ""
		}
		return session.CleanEndpoint(u.Host)
	}
	return ""
}

func CodexSignIn(out string) session.SignIn {
	for line := range strings.SplitSeq(out, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "Not logged in"):
			return session.SignIn{Method: session.SignInNone}
		case strings.HasPrefix(line, "Logged in using ChatGPT"):
			return session.SignIn{Method: session.SignInSubscription}
		case strings.HasPrefix(line, "API key configured"), strings.HasPrefix(line, "Logged in using an API key"):
			return session.SignIn{Method: session.SignInAPIKey}
		case strings.HasPrefix(line, "Logged in using Amazon Bedrock"):
			return session.SignIn{Method: session.SignInBedrock}
		case strings.HasPrefix(line, "Logged in using"):
			return session.SignIn{Method: session.SignInUnknown}
		}
	}
	return session.SignIn{Method: session.SignInUnknown}
}

func (s *Server) signIn(ctx context.Context, q signInJSON) (signInReply, *problem) {
	var res session.SignIn
	switch q.Kind {
	case "claude":
		jsonRun := q.commandJSON
		jsonRun.Argv = append(append([]string(nil), q.Argv...), "--json")
		a, p := s.run(ctx, jsonRun)
		if p != nil {
			return signInReply{}, p
		}
		textRun := q.commandJSON
		textRun.Argv = append(append([]string(nil), q.Argv...), "--text")
		b, p := s.run(ctx, textRun)
		if p != nil {
			return signInReply{}, p
		}
		res = ClaudeSignIn(a.Stdout, b.Stdout)
	case "codex":
		a, p := s.run(ctx, q.commandJSON)
		if p != nil {
			return signInReply{}, p
		}
		res = CodexSignIn(a.Stderr + "\n" + a.Stdout)
	default:
		return signInReply{}, fail(http.StatusBadRequest, "kind", "")
	}
	s.log.Info("agent-host sign-in", "kind", q.Kind, "method", string(res.Method))
	return signInReply{Method: string(res.Method), Endpoint: res.Endpoint}, nil
}

var _ session.SignInChecker = (*Client)(nil)

func (c *Client) SignIn(ctx context.Context, cmd session.Command, kind string) (session.SignIn, error) {
	var out signInReply
	if err := c.call(ctx, http.MethodPost, "/v1/sign-in", signInJSON{commandJSON: wire(cmd), Kind: kind}, &out); err != nil {
		return session.SignIn{}, err
	}
	return session.SignIn{Method: session.MethodOf(out.Method), Endpoint: session.CleanEndpoint(out.Endpoint)}, nil
}
