package agenthost

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/bezmoradi/software-factory/internal/core/session"
)

const (
	memoryRoute     = "/v1/memory"
	cgroupEvents    = "/sys/fs/cgroup/memory.events"
	cgroupMemoryMax = "/sys/fs/cgroup/memory.max"
	maxCgroupFile   = 4 << 10
)

type memoryReply struct {
	OOMKills int64 `json:"oomKills"`
	Limit    int64 `json:"limit"`
}

var _ session.MemoryHost = (*Client)(nil)

func readCgroupFile(path string) ([]byte, bool) {
	f, err := os.Open(path)
	if err != nil {
		return nil, false
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, maxCgroupFile))
	return data, err == nil
}

func oomKillsOf(events []byte) (int64, bool) {
	sc := bufio.NewScanner(bytes.NewReader(events))
	for sc.Scan() {
		key, value, ok := strings.Cut(strings.TrimSpace(sc.Text()), " ")
		if !ok || key != "oom_kill" {
			continue
		}
		n, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
		return n, err == nil && n >= 0
	}
	return 0, false
}

func memoryLimitOf(data []byte) int64 {
	n, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
	if err != nil || n <= 0 {
		return 0
	}
	return n
}

func (s *Server) memory() (memoryReply, *problem) {
	events, ok := readCgroupFile(cgroupEvents)
	if !ok {
		return memoryReply{}, fail(http.StatusUnprocessableEntity, "memory", "agent-host couldn't read this container's memory counters.")
	}
	kills, ok := oomKillsOf(events)
	if !ok {
		return memoryReply{}, fail(http.StatusUnprocessableEntity, "memory", "agent-host couldn't read this container's memory counters.")
	}
	out := memoryReply{OOMKills: kills}
	if limit, ok := readCgroupFile(cgroupMemoryMax); ok {
		out.Limit = memoryLimitOf(limit)
	}
	return out, nil
}

func (s *Server) memoryEvents(w http.ResponseWriter, r *http.Request) {
	var q clearJSON
	if p := decode(w, r, &q); p != nil {
		refuse(w, p)
		return
	}
	if p := s.sameBox(q.Box); p != nil {
		refuse(w, p)
		return
	}
	out, p := s.memory()
	if p != nil {
		refuse(w, p)
		return
	}
	send(w, http.StatusOK, out)
}

func (c *Client) MemoryEvents(ctx context.Context, box string) (session.MemoryEvents, error) {
	var out memoryReply
	if err := c.call(ctx, http.MethodPost, memoryRoute, clearJSON{Box: box}, &out); err != nil {
		return session.MemoryEvents{}, err
	}
	return session.MemoryEvents{OOMKills: out.OOMKills, Limit: out.Limit}, nil
}
