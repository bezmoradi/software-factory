package releases

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/session"
)

const (
	DefaultAPI = "https://api.github.com"
	Repository = "bezmoradi/software-factory"
	UserAgent  = "software-factory-update-check"
	maxAnswer  = 1 << 20
	tagPages   = 5
	perPage    = 100
	callFor    = 15 * time.Second
)

type Source struct {
	Client *http.Client
	API    string
}

var _ session.ReleaseSource = Source{}

func sameOrigin(req *http.Request, via []*http.Request) error {
	if len(via) >= 5 {
		return fmt.Errorf("stopped after %d redirects", len(via))
	}
	if first := via[0].URL; req.URL.Scheme != first.Scheme || req.URL.Host != first.Host {
		return fmt.Errorf("refused a redirect from %s to another host or scheme", first.Host)
	}
	req.Header.Del("Authorization")
	req.Header.Del("Cookie")
	return nil
}

func NewClient() *http.Client {
	return Harden(&http.Client{Transport: &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: 10 * time.Second,
		MaxIdleConnsPerHost:   1,
		IdleConnTimeout:       30 * time.Second,
	}})
}

func Harden(c *http.Client) *http.Client {
	c.Timeout = callFor
	c.CheckRedirect = sameOrigin
	c.Jar = nil
	return c
}

func untrusted(err error) bool {
	if _, ok := errors.AsType[*tls.CertificateVerificationError](err); ok {
		return true
	}
	if _, ok := errors.AsType[x509.UnknownAuthorityError](err); ok {
		return true
	}
	if _, ok := errors.AsType[x509.HostnameError](err); ok {
		return true
	}
	_, ok := errors.AsType[x509.CertificateInvalidError](err)
	return ok
}

func failed(ctx context.Context, err error) error {
	if untrusted(err) {
		return session.ReleaseFailure{Kind: session.UpdateUntrusted}
	}
	if ctx.Err() != nil || errors.Is(err, context.DeadlineExceeded) {
		return session.ReleaseFailure{Kind: session.UpdateTimeout}
	}
	if ne, ok := errors.AsType[net.Error](err); ok && ne.Timeout() {
		return session.ReleaseFailure{Kind: session.UpdateTimeout}
	}
	return session.ReleaseFailure{Kind: session.UpdateOffline}
}

func (s Source) get(ctx context.Context, path string, out any) (bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.API+path, nil)
	if err != nil {
		return false, session.ReleaseFailure{Kind: session.UpdateOffline}
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", UserAgent)
	resp, err := s.Client.Do(req)
	if err != nil {
		return false, failed(ctx, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxAnswer))
		return false, nil
	}
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxAnswer))
		return false, session.ReleaseFailure{Kind: session.UpdateRefused}
	}
	if kind, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type")); err != nil || kind != "application/json" {
		return false, session.ReleaseFailure{Kind: session.UpdateMalformed}
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxAnswer+1))
	if err != nil {
		return false, failed(ctx, err)
	}
	if len(data) > maxAnswer || json.Unmarshal(data, out) != nil {
		return false, session.ReleaseFailure{Kind: session.UpdateMalformed}
	}
	return true, nil
}

type tagJSON struct {
	Name string `json:"name"`
}

func (s Source) Tags(ctx context.Context) ([]string, error) {
	var names []string
	for page := 1; page <= tagPages; page++ {
		var list []tagJSON
		found, err := s.get(ctx, "/repos/"+Repository+"/tags?per_page="+strconv.Itoa(perPage)+"&page="+strconv.Itoa(page), &list)
		if err != nil {
			return nil, err
		}
		if !found {
			return nil, session.ReleaseFailure{Kind: session.UpdateRefused}
		}
		for _, t := range list {
			names = append(names, t.Name)
		}
		if len(list) < perPage {
			break
		}
	}
	return names, nil
}

type releaseJSON struct {
	TagName    string `json:"tag_name"`
	Draft      bool   `json:"draft"`
	PreRelease bool   `json:"prerelease"`
}

func (s Source) Release(ctx context.Context, tag string) (session.ReleaseNotes, error) {
	var r releaseJSON
	found, err := s.get(ctx, "/repos/"+Repository+"/releases/tags/"+url.PathEscape(tag), &r)
	if err != nil || !found {
		return session.ReleaseNotes{}, err
	}
	return session.ReleaseNotes{Found: true, Tag: r.TagName, Draft: r.Draft, PreRelease: r.PreRelease}, nil
}
