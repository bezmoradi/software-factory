package registry

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/bezmoradi/software-factory/internal/core/session"
)

const (
	npmHost     = "https://registry.npmjs.org"
	pypiHost    = "https://pypi.org"
	goProxyHost = "https://proxy.golang.org"
	cratesHost  = "https://crates.io"
	userAgent   = "software-factory (package age check)"
	maxAnswer   = 16 << 20
	maxList     = 1 << 20
	askFor      = 15 * time.Second
	keepFor     = 6 * time.Hour
	maxKept     = 2000
)

var (
	npmName    = regexp.MustCompile(`^(?:@[a-z0-9-~][a-z0-9-._~]*/)?[a-z0-9-~][a-z0-9-._~]*$`)
	pypiName   = regexp.MustCompile(`^(?:[A-Za-z0-9]|[A-Za-z0-9][A-Za-z0-9._-]*[A-Za-z0-9])$`)
	crateName  = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{0,63}$`)
	goPath     = regexp.MustCompile(`^[A-Za-z0-9.~_+-]+(?:/[A-Za-z0-9.~_+-]+)*$`)
	goVersion  = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+(?:[-+][0-9A-Za-z.+-]{1,100})?$`)
	goFirstDot = regexp.MustCompile(`^[a-z0-9-]+(?:\.[a-z0-9-]+)+$`)
)

type Registry struct {
	Client *http.Client
	mu     sync.Mutex
	kept   map[string]kept
}

type kept struct {
	facts session.PackageFacts
	at    time.Time
}

var _ session.PackageRegistry = (*Registry)(nil)

func NewClient() *http.Client {
	return &http.Client{Timeout: askFor, Transport: &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 10 * time.Second}).DialContext,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: askFor,
		MaxIdleConnsPerHost:   2,
	}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func Valid(ecosystem, name string) bool {
	switch ecosystem {
	case session.EcosystemNPM:
		return len(name) <= 214 && npmName.MatchString(name)
	case session.EcosystemPyPI:
		return len(name) <= 200 && pypiName.MatchString(name)
	case session.EcosystemCrate:
		return crateName.MatchString(name)
	case session.EcosystemGo:
		if len(name) > 300 || !goPath.MatchString(name) || strings.Contains(name, "..") || strings.HasSuffix(name, "/") {
			return false
		}
		first, _, _ := strings.Cut(name, "/")
		return goFirstDot.MatchString(first)
	}
	return false
}

func (r *Registry) Lookup(ctx context.Context, ecosystem, name, version string) (session.PackageFacts, error) {
	if !Valid(ecosystem, name) {
		return session.PackageFacts{}, errors.New("the name isn't a valid " + ecosystem + " package name, so it wasn't looked up")
	}
	key := ecosystem + " " + name
	r.mu.Lock()
	if k, ok := r.kept[key]; ok && time.Since(k.at) < keepFor {
		r.mu.Unlock()
		return k.facts, nil
	}
	r.mu.Unlock()
	var facts session.PackageFacts
	var err error
	switch ecosystem {
	case session.EcosystemNPM:
		facts, err = r.npm(ctx, name)
	case session.EcosystemPyPI:
		facts, err = r.pypi(ctx, name)
	case session.EcosystemCrate:
		facts, err = r.crate(ctx, name)
	case session.EcosystemGo:
		facts, err = r.module(ctx, name, version)
	}
	if err != nil {
		return session.PackageFacts{}, err
	}
	r.mu.Lock()
	if r.kept == nil || len(r.kept) >= maxKept {
		r.kept = map[string]kept{}
	}
	r.kept[key] = kept{facts: facts, at: time.Now()}
	r.mu.Unlock()
	return facts, nil
}

func (r *Registry) client() *http.Client {
	if r.Client != nil {
		return r.Client
	}
	return NewClient()
}

func (r *Registry) get(ctx context.Context, raw, accept string, limit int64) ([]byte, bool, error) {
	ctx, cancel := context.WithTimeout(ctx, askFor)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return nil, false, err
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("User-Agent", userAgent)
	host := req.URL.Host
	resp, err := r.client().Do(req)
	if err != nil {
		return nil, false, errors.New(host + " didn't answer")
	}
	defer func() { _ = resp.Body.Close() }()
	switch {
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone:
		return nil, false, nil
	case resp.StatusCode != http.StatusOK:
		return nil, false, errors.New(host + " answered " + http.StatusText(resp.StatusCode))
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, false, errors.New(host + "'s answer couldn't be read")
	}
	if int64(len(data)) > limit {
		return nil, true, nil
	}
	return data, true, nil
}

func earliest(times ...string) time.Time {
	var out time.Time
	for _, t := range times {
		at, err := time.Parse(time.RFC3339Nano, t)
		if err != nil {
			continue
		}
		if out.IsZero() || at.Before(out) {
			out = at
		}
	}
	return out
}

func (r *Registry) npm(ctx context.Context, name string) (session.PackageFacts, error) {
	data, found, err := r.get(ctx, npmHost+"/"+strings.Replace(url.PathEscape(name), "%40", "@", 1), "application/json", maxAnswer)
	if err != nil || !found {
		return session.PackageFacts{Found: found}, err
	}
	var doc struct {
		Time map[string]string `json:"time"`
	}
	if data == nil || json.Unmarshal(data, &doc) != nil {
		return session.PackageFacts{Found: true}, nil
	}
	return session.PackageFacts{Found: true, Published: earliest(doc.Time["created"])}, nil
}

func (r *Registry) pypi(ctx context.Context, name string) (session.PackageFacts, error) {
	data, found, err := r.get(ctx, pypiHost+"/pypi/"+url.PathEscape(name)+"/json", "application/json", maxAnswer)
	if err != nil || !found {
		return session.PackageFacts{Found: found}, err
	}
	var doc struct {
		Releases map[string][]struct {
			Upload string `json:"upload_time_iso_8601"`
		} `json:"releases"`
	}
	if data == nil || json.Unmarshal(data, &doc) != nil {
		return session.PackageFacts{Found: true}, nil
	}
	var times []string
	for _, files := range doc.Releases {
		for _, f := range files {
			times = append(times, f.Upload)
		}
	}
	return session.PackageFacts{Found: true, Published: earliest(times...)}, nil
}

func (r *Registry) crate(ctx context.Context, name string) (session.PackageFacts, error) {
	data, found, err := r.get(ctx, cratesHost+"/api/v1/crates/"+url.PathEscape(name), "application/json", maxAnswer)
	if err != nil || !found {
		return session.PackageFacts{Found: found}, err
	}
	var doc struct {
		Crate struct {
			Created string `json:"created_at"`
		} `json:"crate"`
	}
	if data == nil || json.Unmarshal(data, &doc) != nil {
		return session.PackageFacts{Found: true}, nil
	}
	return session.PackageFacts{Found: true, Published: earliest(doc.Crate.Created)}, nil
}

func escapeModule(path string) string {
	var b strings.Builder
	for _, c := range path {
		if unicode.IsUpper(c) {
			b.WriteByte('!')
			b.WriteRune(unicode.ToLower(c))
			continue
		}
		b.WriteRune(c)
	}
	return b.String()
}

func (r *Registry) module(ctx context.Context, path, version string) (session.PackageFacts, error) {
	base := goProxyHost + "/" + escapeModule(path) + "/@v/"
	list, found, err := r.get(ctx, base+"list", "text/plain", maxList)
	if err != nil || !found {
		return session.PackageFacts{Found: found}, err
	}
	first := ""
	for line := range strings.SplitSeq(string(list), "\n") {
		v := strings.TrimSpace(line)
		if goVersion.MatchString(v) && (first == "" || older(v, first)) {
			first = v
		}
	}
	if first == "" && goVersion.MatchString(version) {
		first = version
	}
	if first == "" {
		return session.PackageFacts{Found: true}, nil
	}
	data, found, err := r.get(ctx, base+escapeModule(first)+".info", "application/json", maxList)
	if err != nil || !found || data == nil {
		return session.PackageFacts{Found: true}, err
	}
	var info struct {
		Time string `json:"Time"`
	}
	if json.Unmarshal(data, &info) != nil {
		return session.PackageFacts{Found: true}, nil
	}
	return session.PackageFacts{Found: true, Published: earliest(info.Time)}, nil
}

func older(a, b string) bool {
	pa, pb := parts(a), parts(b)
	for i := range pa {
		if pa[i] != pb[i] {
			return pa[i] < pb[i]
		}
	}
	return false
}

func parts(v string) [3]int {
	var out [3]int
	core, _, _ := strings.Cut(strings.TrimPrefix(v, "v"), "-")
	core, _, _ = strings.Cut(core, "+")
	for i, p := range strings.SplitN(core, ".", 3) {
		n := 0
		for _, c := range p {
			if c < '0' || c > '9' || n > 1e8 {
				break
			}
			n = n*10 + int(c-'0')
		}
		out[i] = n
	}
	return out
}
