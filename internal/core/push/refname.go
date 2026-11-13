package push

import "strings"

func ValidRefName(name string) bool {
	if name == "HEAD" {
		return true
	}
	if !strings.HasPrefix(name, "refs/") || len(name) > 1024 || strings.HasSuffix(name, "/") || strings.HasSuffix(name, ".") {
		return false
	}
	if strings.Contains(name, "..") || strings.Contains(name, "@{") || strings.Contains(name, "//") {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if c < 0x20 || c == 0x7f || strings.IndexByte(" ~^:?*[\\", c) >= 0 {
			return false
		}
	}
	for part := range strings.SplitSeq(name, "/") {
		if part == "" || strings.HasPrefix(part, ".") || strings.HasSuffix(part, ".lock") {
			return false
		}
	}
	return true
}

func ValidBranch(name string) bool {
	return name != "" && name != "HEAD" && !strings.HasPrefix(name, "-") && ValidRefName("refs/heads/"+name)
}
