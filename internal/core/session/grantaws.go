package session

import (
	"context"
	"errors"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/shared"
)

const (
	AWSCopyMost    = time.Hour
	AWSCopyLeast   = 15 * time.Minute
	AWSRenewBefore = 10 * time.Minute
	awsRetryAfter  = 10 * time.Second
	awsCallFor     = 5 * time.Second
	awsSessionName = "software-factory-"
)

var awsCode = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9.]{0,63}$`)

type AWSCopyAsk struct {
	KeyID     string
	SecretKey string
	Role      string
	Region    string
	Name      string
	Duration  time.Duration
}

type AWSCopy struct {
	KeyID     string
	SecretKey string
	Token     string
	Expires   time.Time
}

type AWSCopies interface {
	Copy(ctx context.Context, q AWSCopyAsk) (AWSCopy, error)
}

type AWSRefused struct {
	Code   string
	Status int
}

func (e AWSRefused) Error() string {
	return "aws refused the copy: " + e.Code + " " + strconv.Itoa(e.Status)
}

type awsCopy struct {
	AWSCopy
	cred  string
	box   string
	tried time.Time
	note  string
}

func copyKey(box string, c Credential) string {
	return box + "/" + c.ID + "/" + strconv.FormatInt(c.Saved.UnixNano(), 10)
}

func AWSHost(region string) string {
	if strings.HasPrefix(region, "cn-") {
		return "sts." + region + ".amazonaws.com.cn"
	}
	return "sts." + region + ".amazonaws.com"
}

func copyDuration(end, now time.Time) time.Duration {
	d := AWSCopyMost
	if !end.IsZero() && end.Sub(now) < d {
		d = end.Sub(now)
	}
	return max(d, AWSCopyLeast).Truncate(time.Second)
}

func copyFailWords(c Credential, err error) string {
	retry := strconv.Itoa(int(ExpireWatch / time.Second))
	if e, ok := errors.AsType[AWSRefused](err); ok {
		code := "HTTP " + strconv.Itoa(e.Status)
		if awsCode.MatchString(e.Code) {
			code = e.Code
		}
		return "AWS refused a short-lived copy of " + c.Name + " (" + code + "), so this session gets none of it; Software Factory asks again every " + retry + " seconds."
	}
	return "Software Factory couldn't get a short-lived copy of " + c.Name + " from " + AWSHost(c.Secret.Region) + ", so this session gets none of it; it asks again every " + retry + " seconds."
}

func copyLive(cp awsCopy, now time.Time) bool {
	return cp.KeyID != "" && now.Before(cp.Expires)
}

func (g *Grants) copyFor(ctx context.Context, box string, c Credential, end, now time.Time) (awsCopy, bool) {
	key := copyKey(box, c)
	g.mu.Lock()
	cur := g.copies[key]
	g.mu.Unlock()
	live := copyLive(cur, now)
	due := !live || cur.Expires.Sub(now) <= AWSRenewBefore && (end.IsZero() || end.After(cur.Expires))
	if due && now.Sub(cur.tried) >= awsRetryAfter {
		cctx, cancel := context.WithTimeout(ctx, awsCallFor)
		got, err := g.cfg.AWS.Copy(cctx, AWSCopyAsk{KeyID: c.Secret.KeyID, SecretKey: c.Secret.SecretKey, Role: c.Secret.Role, Region: c.Secret.Region, Name: awsSessionName + box, Duration: copyDuration(end, now)})
		cancel()
		log := shared.For(ctx, g.cfg.Logger)
		cur.cred, cur.box, cur.tried = c.ID, box, now
		if err == nil && got.KeyID != "" && got.Expires.After(now) {
			cur.AWSCopy, cur.note = got, ""
			log.Info("aws", "event", "copy-made", "credential", c.Name, "box", box, "expires", got.Expires.UTC().Format(time.RFC3339))
		} else {
			cur.note = copyFailWords(c, err)
			code := "unreachable"
			if e, ok := errors.AsType[AWSRefused](err); ok && awsCode.MatchString(e.Code) {
				code = e.Code
			}
			log.Warn("aws", "event", "copy-not-made", "credential", c.Name, "box", box, "why", code)
		}
		g.mu.Lock()
		g.copies[key] = cur
		g.mu.Unlock()
	}
	return cur, copyLive(cur, now)
}

func (g *Grants) copyUntilLocked(cred string, boxes []string, now time.Time) time.Time {
	var until time.Time
	for _, cp := range g.copies {
		if cp.cred != cred || !copyLive(cp, now) {
			continue
		}
		for _, b := range boxes {
			if cp.box == b && cp.Expires.After(until) {
				until = cp.Expires
			}
		}
	}
	return until
}

func (g *Grants) copyNoteLocked(box string, c Credential) string {
	return g.copies[copyKey(box, c)].note
}

func boxesFor(gr Grant, targets []GrantTarget) []string {
	var out []string
	for _, t := range targets {
		if t.Box != "" && covers(gr, t) {
			out = append(out, t.Box)
		}
	}
	return out
}

func CopyKeptWords(until time.Time) string {
	return "A copy the agent took keeps working until " + until.UTC().Format(time.RFC3339) + ", even after the grant ends: AWS can't end it sooner."
}

func (g *Grants) dropCopiesLocked(live, used map[string]bool, now time.Time) {
	for k, cp := range g.copies {
		if !live[cp.box] || !used[k] && !copyLive(cp, now) {
			delete(g.copies, k)
		}
	}
}
