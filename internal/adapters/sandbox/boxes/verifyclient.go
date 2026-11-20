package boxes

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/bezmoradi/software-factory/internal/core/session"
)

var _ session.VerifierHost = (*Client)(nil)

const (
	minVerifyLimit = time.Minute
	maxVerifyLimit = maxVerifyMinutes * time.Minute
)

func verifierPath(run, digest string) (string, error) {
	if !session.ValidBox(run) || (digest != "" && !digestPattern.MatchString(digest)) {
		return "", fmt.Errorf("%w: a verification run is 16 lowercase hexadecimal digits, and an image digest 64", session.ErrBoxRefused)
	}
	if digest == "" {
		return "/v1/verifiers/" + run, nil
	}
	return "/v1/verifiers/" + run + "/" + digest, nil
}

func verifierFrom(data []byte, run string) (*verifierJSON, error) {
	var a verifierAnswerJSON
	if json.Unmarshal(data, &a) != nil || a.Version != WireVersion || a.Verifier == nil {
		return nil, fmt.Errorf("%w: its answer about the verification container isn't one the engine reads", session.ErrBoxUnavailable)
	}
	v := a.Verifier
	limit := time.Duration(v.LimitSeconds) * time.Second
	if v.Run != run || !imageIDPattern.MatchString(v.Image) || !digestPattern.MatchString(v.Digest) || limit < minVerifyLimit || limit > maxVerifyLimit ||
		len(v.State) == 0 || len(v.State) > verifierStateLength || strings.Trim(v.State, "abcdefghijklmnopqrstuvwxyz") != "" ||
		len(v.Error) > 400 || !utf8.ValidString(v.Error) || v.StartedAt < 0 || v.FinishedAt < 0 || (v.Recipe != "" && !validRecipeLabel(v.Recipe)) {
		return nil, fmt.Errorf("%w: its answer about the verification container isn't one the engine reads", session.ErrBoxUnavailable)
	}
	return v, nil
}

func timeOf(ms int64) time.Time {
	if ms <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms)
}

func (c *Client) StartVerifier(ctx context.Context, run, digest string) (session.VerifierInfo, error) {
	if digest == "" {
		return session.VerifierInfo{}, fmt.Errorf("%w: a verification run needs its image's digest", session.ErrBoxRefused)
	}
	p, err := verifierPath(run, digest)
	if err != nil {
		return session.VerifierInfo{}, err
	}
	data, err := c.do(ctx, http.MethodPut, p, maxBoxAnswer, startFor+30*time.Second)
	if err != nil {
		return session.VerifierInfo{}, err
	}
	v, err := verifierFrom(data, run)
	if err != nil {
		return session.VerifierInfo{}, err
	}
	if v.Digest != digest || v.Recipe == "" {
		return session.VerifierInfo{}, fmt.Errorf("%w: its answer names another image than the one asked for", session.ErrBoxUnavailable)
	}
	return session.VerifierInfo{Run: v.Run, Image: v.Image, Digest: v.Digest, Limit: time.Duration(v.LimitSeconds) * time.Second, Recipe: v.Recipe}, nil
}

func (c *Client) VerifierState(ctx context.Context, run string) (session.VerifierState, error) {
	p, err := verifierPath(run, "")
	if err != nil {
		return session.VerifierState{}, err
	}
	data, err := c.do(ctx, http.MethodGet, p, maxBoxAnswer, otherFor+30*time.Second)
	if err != nil {
		return session.VerifierState{}, err
	}
	v, err := verifierFrom(data, run)
	if err != nil {
		return session.VerifierState{}, err
	}
	return session.VerifierState{Run: v.Run, State: v.State, ExitCode: v.ExitCode, OOMKilled: v.OOMKilled, Error: oneLine(v.Error), StartedAt: timeOf(v.StartedAt),
		FinishedAt: timeOf(v.FinishedAt), Image: v.Image, Digest: v.Digest, Limit: time.Duration(v.LimitSeconds) * time.Second}, nil
}

func (c *Client) VerifierLog(ctx context.Context, run string) (string, error) {
	p, err := verifierPath(run, "")
	if err != nil {
		return "", err
	}
	data, err := c.do(ctx, http.MethodGet, p+"/"+logSeg, maxVerifierAnswer, otherFor+30*time.Second)
	if err != nil {
		return "", err
	}
	var l verifierLogJSON
	if json.Unmarshal(data, &l) != nil || l.Version != WireVersion || len(l.Log) > verifierLogKeep*3 || !utf8.ValidString(l.Log) {
		return "", fmt.Errorf("%w: its answer with the verification container's output isn't one the engine reads", session.ErrBoxUnavailable)
	}
	return l.Log, nil
}

func (c *Client) RemoveVerifier(ctx context.Context, run string) error {
	p, err := verifierPath(run, "")
	if err != nil {
		return err
	}
	_, err = c.do(ctx, http.MethodDelete, p, maxBoxAnswer, otherFor+30*time.Second)
	return err
}

func (c *Client) ListVerifiers(ctx context.Context) ([]session.BoxInfo, error) {
	data, err := c.do(ctx, http.MethodGet, "/v1/verifiers", maxListAnswer, otherFor+30*time.Second)
	if err != nil {
		return nil, err
	}
	var l verifiersJSON
	if json.Unmarshal(data, &l) != nil || l.Version != WireVersion || l.Verifiers == nil || len(l.Verifiers) > maxBoxes {
		return nil, fmt.Errorf("%w: its list of verification containers isn't one the engine reads", session.ErrBoxUnavailable)
	}
	out := make([]session.BoxInfo, 0, len(l.Verifiers))
	for _, b := range l.Verifiers {
		info, ok := infoFrom(b)
		if !ok {
			return nil, fmt.Errorf("%w: its list of verification containers isn't one the engine reads", session.ErrBoxUnavailable)
		}
		out = append(out, info)
	}
	return out, nil
}
