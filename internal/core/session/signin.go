package session

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"sync"
	"time"
)

type SignInMethod string

const (
	SignInSubscription SignInMethod = "subscription"
	SignInAPIKey       SignInMethod = "api-key"
	SignInGateway      SignInMethod = "gateway"
	SignInBedrock      SignInMethod = "bedrock"
	SignInVertex       SignInMethod = "vertex"
	SignInNone         SignInMethod = "not-signed-in"
	SignInUnknown      SignInMethod = "unknown"
)

var signInMethods = map[SignInMethod]string{
	SignInSubscription: "a subscription", SignInAPIKey: "an API key", SignInGateway: "a gateway (a custom base URL)",
	SignInBedrock: "Amazon Bedrock", SignInVertex: "Google Vertex AI", SignInNone: "not signed in", SignInUnknown: "a method the engine doesn't recognise",
}

func MethodOf(s string) SignInMethod {
	m := SignInMethod(s)
	if _, ok := signInMethods[m]; ok {
		return m
	}
	return SignInUnknown
}

func (m SignInMethod) Words() string {
	if w, ok := signInMethods[m]; ok {
		return w
	}
	return signInMethods[SignInUnknown]
}

type SignIn struct {
	Method    SignInMethod
	Endpoint  string
	CheckedAt time.Time
}

func (s SignIn) Costs() bool {
	return s.Method != SignInSubscription && s.Method != SignInNone
}

func (s SignIn) Words() string {
	w := s.Method.Words()
	if s.Endpoint != "" {
		w += " at " + s.Endpoint
	}
	return w
}

var endpointHost = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,62})(\.[a-z0-9]([a-z0-9-]{0,62}))*(:[0-9]{1,5})?$`)

func CleanEndpoint(host string) string {
	host = strings.ToLower(strings.TrimSpace(host))
	if len(host) > 253 || !endpointHost.MatchString(host) {
		return ""
	}
	return host
}

type Expect string

const (
	ExpectAny          Expect = "any"
	ExpectSubscription Expect = "subscription"
	ExpectAPIKey       Expect = "api-key"
)

func ValidExpect(e Expect) bool {
	return e == ExpectAny || e == ExpectSubscription || e == ExpectAPIKey
}

func (e Expect) Words() string {
	switch e {
	case ExpectSubscription:
		return "a subscription"
	case ExpectAPIKey:
		return "an API key (or a gateway, Bedrock or Vertex)"
	}
	return "any sign-in"
}

func (e Expect) Accepts(m SignInMethod) bool {
	switch e {
	case ExpectSubscription:
		return m == SignInSubscription
	case ExpectAPIKey:
		return m == SignInAPIKey || m == SignInGateway || m == SignInBedrock || m == SignInVertex
	}
	return m != SignInNone
}

type SignInChecker interface {
	SignIn(ctx context.Context, c Command, kind string) (SignIn, error)
}

type Expectations interface {
	Load() (map[string]Expect, error)
	Save(map[string]Expect) error
}

const ReviewerAgent = "reviewer"

const ReviewerSignInStep = "press Sign Claude Code in under Finish setting up on the Welcome page, or run docker compose exec -it reviewer claude, then /login"

const signInFor = 30 * time.Second

type SignInStatus struct {
	Args []string
	Kind string
	Step string
}

type Expected struct {
	Store Expectations
	mu    sync.Mutex
}

func (e *Expected) Of(agent string) Expect {
	if e == nil || e.Store == nil {
		return ExpectAny
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	all, err := e.Store.Load()
	if err != nil {
		return ExpectAny
	}
	if x, ok := all[agent]; ok && ValidExpect(x) {
		return x
	}
	return ExpectAny
}

func (e *Expected) All() map[string]Expect {
	out := map[string]Expect{}
	if e == nil || e.Store == nil {
		return out
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	all, err := e.Store.Load()
	if err != nil {
		return out
	}
	for k, v := range all {
		if ValidExpect(v) {
			out[k] = v
		}
	}
	return out
}

var errNoExpectations = errors.New("this engine keeps no sign-in expectations")

func (e *Expected) Set(agent string, x Expect) error {
	if e == nil || e.Store == nil {
		return errNoExpectations
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	all, err := e.Store.Load()
	if err != nil {
		return err
	}
	if all == nil {
		all = map[string]Expect{}
	}
	if x == ExpectAny {
		delete(all, agent)
	} else {
		all[agent] = x
	}
	return e.Store.Save(all)
}

func (e *Expected) Adopt(agent string, m SignInMethod) Expect {
	var kind Expect
	switch m {
	case SignInSubscription:
		kind = ExpectSubscription
	case SignInAPIKey, SignInGateway, SignInBedrock, SignInVertex:
		kind = ExpectAPIKey
	}
	if e == nil || e.Store == nil {
		return ExpectAny
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	all, err := e.Store.Load()
	if err != nil {
		return ExpectAny
	}
	if x, ok := all[agent]; ok && ValidExpect(x) {
		return x
	}
	if kind == "" {
		return ExpectAny
	}
	if all == nil {
		all = map[string]Expect{}
	}
	all[agent] = kind
	if e.Store.Save(all) != nil {
		return ExpectAny
	}
	return kind
}

func judge(name string, s SignIn, want Expect, step string, costOK bool, what string) error {
	switch {
	case s.Method == SignInNone:
		return &Error{Kind: Conflict, Reason: "not signed in", Message: name + " isn't signed in, so " + what + " doesn't start. Sign it in with its own sign-in: " + step + ".",
			Needs: &Needs{SignIn: &s, Expected: want, Step: step}}
	case !want.Accepts(s.Method):
		return &Error{Kind: Conflict, Reason: "sign-in", Message: name + " is signed in with " + s.Words() + ", but you expect " + want.Words() + ", so " + what + " doesn't start. Sign it in the way you expect with its own sign-in (" + step + "), or change what you expect.",
			Needs: &Needs{SignIn: &s, Expected: want, Step: step}}
	case s.Costs() && !costOK:
		return &Error{Kind: Conflict, Reason: "cost", Message: name + " is signed in with " + s.Words() + ": " + what + " runs on it and costs money. Confirm to start it.",
			Needs: &Needs{SignIn: &s, Expected: want, Cost: true}}
	}
	return nil
}
