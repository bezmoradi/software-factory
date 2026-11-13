package session

import (
	"encoding/json"
	"errors"
	"math"
	"slices"
	"strings"
)

func reviewHostProblem(err error) (string, string) {
	h, ok := errors.AsType[*HostError](err)
	if !ok {
		return "the engine couldn't reach the reviewer container (" + clip(err.Error(), 200) + "); check that it's running", "host-unreachable"
	}
	code := clip(h.Code, 40)
	switch code {
	case "argv", "env", "body":
		return "the reviewer container refused the engine's request as malformed, a Software Factory bug rather than a problem with your change (" + hostMessage(err) + ")", "host-refused:" + code
	}
	return "the reviewer container refused to run it (" + hostMessage(err) + ")", "host-refused:" + code
}

type result struct {
	text    string
	subtype string
	failed  bool
	cost    float64
	turns   int
	denied  []string
	outside []string
	models  []string
}

func claudeResult(stdout string) (result, bool) {
	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	candidates := []string{strings.TrimSpace(stdout)}
	for _, l := range slices.Backward(lines) {
		candidates = append(candidates, l)
	}
	for _, c := range candidates {
		c = strings.TrimSpace(c)
		if !strings.HasPrefix(c, "{") {
			continue
		}
		var m map[string]any
		if json.Unmarshal([]byte(c), &m) != nil || m["type"] != "result" {
			continue
		}
		text, _ := m["result"].(string)
		subtype, _ := m["subtype"].(string)
		isError, _ := m["is_error"].(bool)
		cost, _ := m["total_cost_usd"].(float64)
		if math.IsNaN(cost) || cost < 0 || cost > 1e7 {
			cost = 0
		}
		out := result{text: text, subtype: subtype, failed: isError || subtype != "success", cost: cost, models: modelsOf(m["modelUsage"]), turns: -1}
		if n, ok := m["num_turns"].(float64); ok && n >= 0 && n <= 1e5 && n == math.Trunc(n) {
			out.turns = int(n)
		}
		out.denied, out.outside = denialsOf(m["permission_denials"])
		return out, true
	}
	return result{}, false
}
