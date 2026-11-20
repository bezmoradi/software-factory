package boxes

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	APIVersion     = "1.48"
	apiPrefix      = "/v" + APIVersion
	maxDockerReply = 8 << 20
	maxDockerError = 4 << 10
	dockerCallFor  = 2 * time.Minute
)

type Docker struct {
	client *http.Client
	stream *http.Client
}

type DockerError struct {
	Code    int
	Message string
}

func (e *DockerError) Error() string {
	return "docker answered " + strconv.Itoa(e.Code) + ": " + e.Message
}

func IsNotFound(err error) bool {
	var de *DockerError
	return errors.As(err, &de) && de.Code == http.StatusNotFound
}

func NewDocker(socket string) *Docker {
	dial := func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "unix", socket)
	}
	return &Docker{
		client: &http.Client{Timeout: dockerCallFor, Transport: &http.Transport{Proxy: nil, DialContext: dial, MaxIdleConns: 4, IdleConnTimeout: 30 * time.Second, MaxResponseHeaderBytes: 64 << 10}},
		stream: &http.Client{Transport: &http.Transport{Proxy: nil, DialContext: dial, DisableKeepAlives: true, MaxResponseHeaderBytes: 64 << 10}},
	}
}

type versionJSON struct {
	APIVersion    string `json:"ApiVersion"`
	MinAPIVersion string `json:"MinAPIVersion"`
	Version       string `json:"Version"`
}

func (d *Docker) CheckVersion(ctx context.Context) (string, error) {
	var v versionJSON
	if err := d.call(ctx, http.MethodGet, "/version", nil, nil, &v, false); err != nil {
		if errors.Is(err, fs.ErrPermission) {
			return "", fmt.Errorf("the box helper may not use Docker's socket (permission denied): set SF_DOCKER_GID to the group that owns the socket on the Docker host, which stat -c %%g /var/run/docker.sock shows there (2375 inside Docker-in-Docker, 0 on Docker Desktop): %w", err)
		}
		return "", fmt.Errorf("the box helper can't reach Docker's API on its socket: %w", err)
	}
	if !versionWithin(APIVersion, v.MinAPIVersion, v.APIVersion) {
		return "", fmt.Errorf("the box helper needs Docker Engine API %s (Engine 28.0 or later), and this Docker answers API %s to %s", APIVersion, v.MinAPIVersion, v.APIVersion)
	}
	return v.Version, nil
}

func versionWithin(want, low, high string) bool {
	w, okW := parseVersion(want)
	l, okL := parseVersion(low)
	h, okH := parseVersion(high)
	if !okW || !okL || !okH {
		return false
	}
	return !less(w, l) && !less(h, w)
}

func parseVersion(s string) ([2]int, bool) {
	major, minor, ok := strings.Cut(s, ".")
	if !ok {
		return [2]int{}, false
	}
	a, errA := strconv.Atoi(major)
	b, errB := strconv.Atoi(minor)
	if errA != nil || errB != nil || a < 0 || b < 0 {
		return [2]int{}, false
	}
	return [2]int{a, b}, true
}

func less(a, b [2]int) bool {
	return a[0] < b[0] || (a[0] == b[0] && a[1] < b[1])
}

func filters(pairs map[string][]string) url.Values {
	data, _ := json.Marshal(pairs)
	return url.Values{"filters": {string(data)}}
}

func (d *Docker) call(ctx context.Context, method, path string, query url.Values, body, out any, versioned bool) error {
	resp, err := d.send(ctx, d.client, method, path, query, body, versioned)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 && resp.StatusCode != http.StatusNotModified {
		return readDockerError(resp)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxDockerReply+1))
	if err != nil {
		return fmt.Errorf("the answer from Docker couldn't be read: %w", err)
	}
	if len(data) > maxDockerReply {
		return errors.New("the answer from Docker is larger than the box helper reads")
	}
	if out == nil || len(bytes.TrimSpace(data)) == 0 {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("the answer from Docker to %s %s isn't JSON the box helper reads: %w", method, path, err)
	}
	return nil
}

func (d *Docker) send(ctx context.Context, client *http.Client, method, path string, query url.Values, body any, versioned bool) (*http.Response, error) {
	var r io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		r = bytes.NewReader(data)
	}
	u := url.URL{Scheme: "http", Host: "docker", Path: path, RawQuery: query.Encode()}
	if versioned {
		u.Path = apiPrefix + path
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), r)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("the Docker socket didn't answer: %w", err)
	}
	return resp, nil
}

func readDockerError(resp *http.Response) error {
	data, _ := io.ReadAll(io.LimitReader(resp.Body, maxDockerError))
	var e struct {
		Message string `json:"message"`
	}
	msg := strings.TrimSpace(string(data))
	if json.Unmarshal(data, &e) == nil && e.Message != "" {
		msg = e.Message
	}
	return &DockerError{Code: resp.StatusCode, Message: oneLine(msg)}
}

func oneLine(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, s)
	if len(s) > 300 {
		s = s[:300]
	}
	return strings.TrimSpace(s)
}

func (d *Docker) get(ctx context.Context, path string, query url.Values, out any) error {
	return d.call(ctx, http.MethodGet, path, query, nil, out, true)
}

func (d *Docker) post(ctx context.Context, path string, query url.Values, body, out any) error {
	return d.call(ctx, http.MethodPost, path, query, body, out, true)
}

func (d *Docker) delete(ctx context.Context, path string, query url.Values) error {
	return d.call(ctx, http.MethodDelete, path, query, nil, nil, true)
}

func (d *Docker) Events(ctx context.Context, query url.Values, each func(eventJSON)) error {
	resp, err := d.send(ctx, d.stream, http.MethodGet, "/events", query, nil, true)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return readDockerError(resp)
	}
	dec := json.NewDecoder(resp.Body)
	for {
		var ev eventJSON
		if err := dec.Decode(&ev); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("the Docker event stream ended: %w", err)
		}
		each(ev)
	}
}

func pathPart(s string) string {
	if s == "" || s == "." || s == ".." {
		return "-"
	}
	for _, c := range s {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_', c == '.', c == ':':
		default:
			return "-"
		}
	}
	return s
}

func (d *Docker) logTail(ctx context.Context, id string, keep int, limit int64) ([]byte, error) {
	q := url.Values{"stdout": {"1"}, "stderr": {"1"}, "tail": {"400"}}
	resp, err := d.send(ctx, d.client, http.MethodGet, "/containers/"+pathPart(id)+"/logs", q, nil, true)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 {
		return nil, readDockerError(resp)
	}
	body := io.LimitReader(resp.Body, limit)
	var out []byte
	var header [8]byte
	for {
		if _, err := io.ReadFull(body, header[:]); err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				break
			}
			return nil, fmt.Errorf("the container's log couldn't be read: %w", err)
		}
		size := int64(binary.BigEndian.Uint32(header[4:8]))
		chunk := make([]byte, min(size, int64(keep)))
		skip := size - int64(len(chunk))
		if skip > 0 {
			if _, err := io.CopyN(io.Discard, body, skip); err != nil {
				break
			}
		}
		n, err := io.ReadFull(body, chunk)
		out = append(out, chunk[:n]...)
		if len(out) > 2*keep {
			out = append([]byte(nil), out[len(out)-keep:]...)
		}
		if err != nil {
			break
		}
	}
	if len(out) > keep {
		out = out[len(out)-keep:]
	}
	return out, nil
}
