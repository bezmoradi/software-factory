package osv

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	DefaultAPI  = "https://api.osv.dev"
	maxAnswer   = 1 << 20
	maxIDs      = 20
	maxSummary  = 300
	maxAliases  = 8
	lookupFor   = 60 * time.Second
	recordLinks = "https://osv.dev/vulnerability/"
)

var vulnID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,80}$`)

type Query struct {
	Label     string
	Ecosystem string
	Name      string
	Version   string
	Commit    string
}

func (q Query) Sent() string {
	if q.Commit != "" {
		return "commit " + q.Commit
	}
	return q.Ecosystem + " " + q.Name + "@" + q.Version
}

type Finding struct {
	Label   string
	ID      string
	Summary string
	Aliases []string
	Link    string
}

type packageJSON struct {
	Name      string `json:"name"`
	Ecosystem string `json:"ecosystem"`
}

type queryJSON struct {
	Package *packageJSON `json:"package,omitempty"`
	Version string       `json:"version,omitempty"`
	Commit  string       `json:"commit,omitempty"`
}

type batchJSON struct {
	Results []struct {
		Vulns []struct {
			ID string `json:"id"`
		} `json:"vulns"`
	} `json:"results"`
}

type vulnJSON struct {
	ID        string   `json:"id"`
	Summary   string   `json:"summary"`
	Aliases   []string `json:"aliases"`
	Withdrawn string   `json:"withdrawn"`
}

func NewClient() *http.Client {
	return &http.Client{Timeout: lookupFor, Transport: &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 15 * time.Second}).DialContext,
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		MaxIdleConnsPerHost:   1,
	}}
}

func call(ctx context.Context, client *http.Client, method, raw string, in, out any) error {
	var body io.Reader
	if in != nil {
		data, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, raw, body)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("OSV didn't answer")
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxAnswer+1))
	if err != nil || len(data) > maxAnswer {
		return fmt.Errorf("OSV's answer couldn't be read")
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("OSV answered %d", resp.StatusCode)
	}
	if json.Unmarshal(data, out) != nil {
		return fmt.Errorf("OSV's answer isn't the JSON this engine reads")
	}
	return nil
}

func clip(s string, n int) string {
	s = strings.ToValidUTF8(strings.Join(strings.Fields(s), " "), "")
	if len(s) <= n {
		return s
	}
	s = s[:n]
	for !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s + "…"
}

func Lookup(ctx context.Context, client *http.Client, api string, queries []Query) ([]Finding, int, error) {
	api = strings.TrimSuffix(api, "/")
	in := struct {
		Queries []queryJSON `json:"queries"`
	}{}
	for _, q := range queries {
		if q.Commit != "" {
			in.Queries = append(in.Queries, queryJSON{Commit: q.Commit})
			continue
		}
		in.Queries = append(in.Queries, queryJSON{Package: &packageJSON{Name: q.Name, Ecosystem: q.Ecosystem}, Version: q.Version})
	}
	var out batchJSON
	if err := call(ctx, client, http.MethodPost, api+"/v1/querybatch", in, &out); err != nil {
		return nil, 0, err
	}
	if len(out.Results) != len(queries) {
		return nil, 0, fmt.Errorf("OSV answered %d results for %d questions", len(out.Results), len(queries))
	}
	var found []Finding
	more := 0
	details := map[string]vulnJSON{}
	for i, r := range out.Results {
		for _, v := range r.Vulns {
			if !vulnID.MatchString(v.ID) {
				continue
			}
			if len(found) >= maxIDs {
				more++
				continue
			}
			d, ok := details[v.ID]
			if !ok {
				if err := call(ctx, client, http.MethodGet, api+"/v1/vulns/"+url.PathEscape(v.ID), nil, &d); err != nil || d.ID != v.ID {
					d = vulnJSON{ID: v.ID}
				}
				details[v.ID] = d
			}
			if d.Withdrawn != "" {
				continue
			}
			aliases := []string{}
			for _, a := range d.Aliases {
				if vulnID.MatchString(a) && len(aliases) < maxAliases && !slices.Contains(aliases, a) {
					aliases = append(aliases, a)
				}
			}
			found = append(found, Finding{Label: queries[i].Label, ID: v.ID, Summary: clip(d.Summary, maxSummary), Aliases: aliases, Link: recordLinks + v.ID})
		}
	}
	return found, more, nil
}

type Cited struct {
	Client *http.Client
	API    string
}

func (c Cited) Held(ctx context.Context, ids []string) (map[string]bool, error) {
	api := strings.TrimSuffix(c.API, "/")
	out := map[string]bool{}
	for _, id := range ids[:min(len(ids), maxIDs)] {
		if !vulnID.MatchString(id) {
			continue
		}
		held, err := heldID(ctx, c.Client, api, id)
		if err != nil {
			return nil, err
		}
		out[id] = held
	}
	return out, nil
}

func heldID(ctx context.Context, client *http.Client, api, id string) (bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, api+"/v1/vulns/"+url.PathEscape(id), nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return false, fmt.Errorf("OSV didn't answer")
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxAnswer+1))
	if err != nil || len(data) > maxAnswer {
		return false, fmt.Errorf("OSV's answer couldn't be read")
	}
	switch resp.StatusCode {
	case http.StatusNotFound:
		return false, nil
	case http.StatusOK:
	default:
		return false, fmt.Errorf("OSV answered %d", resp.StatusCode)
	}
	var v vulnJSON
	if json.Unmarshal(data, &v) != nil {
		return false, fmt.Errorf("OSV's answer isn't the JSON this engine reads")
	}
	return strings.EqualFold(v.ID, id) || slices.ContainsFunc(v.Aliases, func(a string) bool { return strings.EqualFold(a, id) }), nil
}
