package session

import "strconv"

func claudePortEnv(base int) map[string]string {
	if base == 0 {
		return nil
	}
	first := strconv.Itoa(base)
	return map[string]string{"PORT": first, "SF_PORT_BASE": first}
}
