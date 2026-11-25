package boxes

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/netip"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/bezmoradi/software-factory/internal/core/session"
)

func actionPath(run, box string) (string, error) {
	if !session.ValidBox(run) || box != "" && !session.ValidBox(box) {
		return "", fmt.Errorf("%w: an action run and a box are 16 lowercase hexadecimal digits", session.ErrBoxRefused)
	}
	if box == "" {
		return "/v1/actions/" + run, nil
	}
	return "/v1/actions/" + run + "/" + box, nil
}

func actionFrom(data []byte, run string) (*actionJSON, error) {
	var a actionAnswerJSON
	if json.Unmarshal(data, &a) != nil || a.Version != WireVersion || a.Action == nil {
		return nil, fmt.Errorf("%w: its answer about the action container isn't one the engine reads", session.ErrBoxUnavailable)
	}
	v := a.Action
	if v.Run != run || len(v.State) == 0 || len(v.State) > verifierStateLength || strings.Trim(v.State, "abcdefghijklmnopqrstuvwxyz") != "" ||
		len(v.Error) > 400 || !utf8.ValidString(v.Error) || v.StartedAt < 0 || v.FinishedAt < 0 || (v.Recipe != "" && !validRecipeLabel(v.Recipe)) {
		return nil, fmt.Errorf("%w: its answer about the action container isn't one the engine reads", session.ErrBoxUnavailable)
	}
	return v, nil
}

func (c *Client) MakeAction(ctx context.Context, run, box string) (session.ActionInfo, error) {
	p, err := actionPath(run, box)
	if err != nil {
		return session.ActionInfo{}, err
	}
	data, err := c.do(ctx, http.MethodPut, p, maxBoxAnswer, startFor+30*time.Second)
	if err != nil {
		return session.ActionInfo{}, err
	}
	v, err := actionFrom(data, run)
	if err != nil {
		return session.ActionInfo{}, err
	}
	subnet, perr := netip.ParsePrefix(v.Subnet)
	if perr != nil || subnet.Bits() != boxBits || !subnet.Addr().Is4() || v.Recipe == "" {
		return session.ActionInfo{}, fmt.Errorf("%w: its answer names no network of the action container's own", session.ErrBoxUnavailable)
	}
	return session.ActionInfo{Run: v.Run, Subnet: subnet.Masked(), Recipe: v.Recipe}, nil
}

func (c *Client) StartAction(ctx context.Context, run string) error {
	p, err := actionPath(run, "")
	if err != nil {
		return err
	}
	_, err = c.do(ctx, http.MethodPost, p+"/"+actionStartSeg, maxBoxAnswer, startFor+30*time.Second)
	return err
}

func (c *Client) ActionState(ctx context.Context, run string) (session.ActionState, error) {
	p, err := actionPath(run, "")
	if err != nil {
		return session.ActionState{}, err
	}
	data, err := c.do(ctx, http.MethodGet, p, maxBoxAnswer, otherFor+30*time.Second)
	if err != nil {
		return session.ActionState{}, err
	}
	v, err := actionFrom(data, run)
	if err != nil {
		return session.ActionState{}, err
	}
	return session.ActionState{Run: v.Run, State: v.State, ExitCode: v.ExitCode, OOMKilled: v.OOMKilled, Error: oneLine(v.Error), StartedAt: timeOf(v.StartedAt), FinishedAt: timeOf(v.FinishedAt)}, nil
}

func (c *Client) ActionLog(ctx context.Context, run string) (string, bool, error) {
	p, err := actionPath(run, "")
	if err != nil {
		return "", false, err
	}
	data, err := c.do(ctx, http.MethodGet, p+"/"+logSeg, maxVerifierAnswer, otherFor+30*time.Second)
	if err != nil {
		return "", false, err
	}
	var l actionLogJSON
	if json.Unmarshal(data, &l) != nil || l.Version != WireVersion || len(l.Log) > session.MaxActionOutput*3 || !utf8.ValidString(l.Log) {
		return "", false, fmt.Errorf("%w: its answer with the action container's output isn't one the engine reads", session.ErrBoxUnavailable)
	}
	return l.Log, l.Cut, nil
}

func (c *Client) RemoveAction(ctx context.Context, run string) error {
	p, err := actionPath(run, "")
	if err != nil {
		return err
	}
	_, err = c.do(ctx, http.MethodDelete, p, maxBoxAnswer, otherFor+30*time.Second)
	return err
}

func (c *Client) ListActions(ctx context.Context) ([]session.BoxInfo, error) {
	data, err := c.do(ctx, http.MethodGet, "/v1/actions", maxListAnswer, otherFor+30*time.Second)
	if err != nil {
		return nil, err
	}
	var l actionsJSON
	if json.Unmarshal(data, &l) != nil || l.Version != WireVersion || l.Actions == nil || len(l.Actions) > maxBoxes {
		return nil, fmt.Errorf("%w: its list of action containers isn't one the engine reads", session.ErrBoxUnavailable)
	}
	out := make([]session.BoxInfo, 0, len(l.Actions))
	for _, b := range l.Actions {
		info, ok := infoFrom(b)
		if !ok {
			return nil, fmt.Errorf("%w: its list of action containers isn't one the engine reads", session.ErrBoxUnavailable)
		}
		out = append(out, info)
	}
	return out, nil
}
