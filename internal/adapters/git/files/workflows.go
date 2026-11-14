package files

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"go.yaml.in/yaml/v3"

	"github.com/bezmoradi/software-factory/internal/core/push"
	"github.com/bezmoradi/software-factory/internal/core/session"
)

const (
	maxYAMLDepth     = 64
	maxFilterEntries = 200
)

var (
	_ session.WorkflowReader = Reader{}
	_ session.ChangeBlobs    = Reader{}
)

func (Reader) Blobs(path string, shas []string, limit, budget int64) (map[string]session.BlobData, error) {
	return with(path, func(r *repo) (map[string]session.BlobData, error) {
		out := map[string]session.BlobData{}
		var used int64
		for _, sha := range shas {
			if _, done := out[sha]; done {
				continue
			}
			data, err := r.blob(sha)
			if err != nil {
				if errors.Is(err, push.ErrTooLarge) {
					continue
				}
				return out, err
			}
			b := session.BlobData{Data: data}
			if int64(len(data)) > limit {
				b = session.BlobData{Data: data[:limit], Cut: true}
			}
			used += int64(len(b.Data))
			if used > budget {
				return out, nil
			}
			out[sha] = b
		}
		return out, nil
	})
}

func (Reader) Workflows(path, commit string) ([]session.WorkflowFile, error) {
	return with(path, func(r *repo) ([]session.WorkflowFile, error) {
		c, err := r.commit(commit)
		if err != nil {
			return nil, err
		}
		tree := c.Tree
		for _, part := range []string{".github", "workflows"} {
			list, err := r.tree(tree)
			if err != nil {
				return nil, err
			}
			next := ""
			for _, e := range list {
				if e.name == part && e.mode == push.ModeFolder {
					next = e.sha
				}
			}
			if next == "" {
				return nil, nil
			}
			tree = next
		}
		list, err := r.tree(tree)
		if err != nil {
			return nil, err
		}
		var out []session.WorkflowFile
		for _, e := range list {
			lower := strings.ToLower(e.name)
			if !strings.HasSuffix(lower, ".yml") && !strings.HasSuffix(lower, ".yaml") {
				continue
			}
			if len(out) >= session.MaxWorkflowFiles {
				break
			}
			f := session.WorkflowFile{Path: ".github/workflows/" + e.name, SHA: e.sha}
			switch e.mode {
			case push.ModeFile, push.ModeExecutable:
				data, err := r.blob(e.sha)
				switch {
				case err != nil:
					f.Problem = "couldn't read: " + cause(err)
				case len(data) > session.MaxWorkflowBytes:
					f.Problem = fmt.Sprintf("couldn't read: larger than %d KiB", session.MaxWorkflowBytes>>10)
				case !utf8.Valid(data):
					f.Problem = "couldn't read: not UTF-8 text"
				default:
					f.Text = string(data)
					parseWorkflow(&f, data)
				}
			default:
				f.Problem = "couldn't read: it is a " + string(e.mode) + ", not a file"
			}
			out = append(out, f)
		}
		sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
		return out, nil
	})
}

func parseWorkflow(f *session.WorkflowFile, data []byte) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		f.Problem = "couldn't read: " + session.CleanLine(err.Error(), 200)
		return
	}
	if len(doc.Content) == 0 {
		f.Problem = "couldn't read: the file is empty"
		return
	}
	top := deref(doc.Content[0], 0)
	if top == nil || top.Kind != yaml.MappingNode {
		f.Problem = "couldn't read: the file isn't a mapping"
		return
	}
	on, found := false, false
	for i := 0; i+1 < len(top.Content); i += 2 {
		key, value := deref(top.Content[i], 0), deref(top.Content[i+1], 0)
		if key == nil || value == nil {
			continue
		}
		switch key.Value {
		case "name":
			if value.Kind == yaml.ScalarNode {
				f.Name = value.Value
			}
		case "on", "true":
			if key.Value == "true" && key.Tag != "!!bool" {
				continue
			}
			found = true
			events, ok := triggers(value)
			if !ok {
				f.Problem = "couldn't read its on: key"
				return
			}
			f.Events = events
			on = true
		case "permissions":
			f.IDToken = f.IDToken || idToken(value)
		case "jobs":
			jobs(f, value)
		}
	}
	if !found || !on {
		f.Problem = "couldn't read: it has no on: key"
	}
}

func deref(n *yaml.Node, depth int) *yaml.Node {
	for n != nil && n.Kind == yaml.AliasNode {
		if depth > maxYAMLDepth {
			return nil
		}
		n, depth = n.Alias, depth+1
	}
	return n
}

func scalars(n *yaml.Node) ([]string, bool) {
	n = deref(n, 0)
	switch {
	case n == nil:
		return nil, false
	case n.Kind == yaml.ScalarNode:
		if n.Tag == "!!null" {
			return nil, true
		}
		return []string{n.Value}, true
	case n.Kind == yaml.SequenceNode:
		var out []string
		for _, c := range n.Content {
			c = deref(c, 0)
			if c == nil || c.Kind != yaml.ScalarNode || len(out) >= maxFilterEntries {
				return nil, false
			}
			out = append(out, c.Value)
		}
		return out, true
	}
	return nil, false
}

func triggers(n *yaml.Node) ([]session.WorkflowEvent, bool) {
	if n.Kind == yaml.ScalarNode || n.Kind == yaml.SequenceNode {
		names, ok := scalars(n)
		if !ok {
			return nil, false
		}
		out := make([]session.WorkflowEvent, 0, len(names))
		for _, name := range names {
			out = append(out, session.WorkflowEvent{Name: name})
		}
		return out, true
	}
	if n.Kind != yaml.MappingNode {
		return nil, false
	}
	var out []session.WorkflowEvent
	for i := 0; i+1 < len(n.Content); i += 2 {
		key, value := deref(n.Content[i], 0), deref(n.Content[i+1], 0)
		if key == nil || value == nil || key.Kind != yaml.ScalarNode {
			return nil, false
		}
		e := session.WorkflowEvent{Name: key.Value}
		if value.Kind == yaml.MappingNode {
			for j := 0; j+1 < len(value.Content); j += 2 {
				k, v := deref(value.Content[j], 0), value.Content[j+1]
				if k == nil {
					return nil, false
				}
				switch k.Value {
				case "branches":
					list, ok := scalars(v)
					if !ok {
						return nil, false
					}
					e.Branches, e.HasBranches = list, true
				case "branches-ignore":
					list, ok := scalars(v)
					if !ok {
						return nil, false
					}
					e.BranchesIgnore, e.HasIgnore = list, true
				case "tags", "tags-ignore":
					e.HasTags = true
				case "paths", "paths-ignore":
					e.HasPaths = true
				}
			}
		}
		out = append(out, e)
	}
	return out, true
}

func idToken(n *yaml.Node) bool {
	n = deref(n, 0)
	switch {
	case n == nil:
		return false
	case n.Kind == yaml.ScalarNode:
		return n.Value == "write-all"
	case n.Kind == yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			k, v := deref(n.Content[i], 0), deref(n.Content[i+1], 0)
			if k != nil && v != nil && k.Value == "id-token" && v.Kind == yaml.ScalarNode && v.Value == "write" {
				return true
			}
		}
	}
	return false
}

func jobs(f *session.WorkflowFile, n *yaml.Node) {
	if n.Kind != yaml.MappingNode {
		return
	}
	for i := 1; i < len(n.Content); i += 2 {
		job := deref(n.Content[i], 0)
		if job == nil || job.Kind != yaml.MappingNode {
			continue
		}
		for j := 0; j+1 < len(job.Content); j += 2 {
			k, v := deref(job.Content[j], 0), deref(job.Content[j+1], 0)
			if k == nil || v == nil {
				continue
			}
			switch k.Value {
			case "permissions":
				f.IDToken = f.IDToken || idToken(v)
			case "secrets":
				if v.Kind == yaml.ScalarNode && v.Value == "inherit" {
					f.Inherit = true
				}
			}
		}
	}
}
