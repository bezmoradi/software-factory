package session

import (
	"slices"

	"github.com/bezmoradi/software-factory/internal/core/shared"
)

const MaxRemovedProjects = 50

type RemovedProject[T any] struct {
	ID   ProjectID
	Data T
}

type ProjectData interface {
	ParkProject(id ProjectID)
	RestoreProject(id ProjectID)
	KeepProjects(names []string)
}

func (c *Repos) UseProjectData(data ProjectData) {
	c.OnRemoved(data.ParkProject)
	c.OnAdded(data.RestoreProject)
	list, err := c.List()
	if err != nil {
		c.cfg.Logger.Warn("repository", "event", "project-data-not-checked", "err", err.Error())
		return
	}
	names := make([]string, 0, len(list))
	for _, r := range list {
		names = append(names, r.Name)
	}
	data.KeepProjects(names)
}

func projectDataNotSaved(logger shared.Logger, area, name string, err error) {
	args := []any{"event", "project-data-not-saved"}
	if name != "" {
		args = append(args, "name", name)
	}
	logger.Warn(area, append(args, "err", err.Error())...)
}

func RoomForRemoved[T any](removed []RemovedProject[T], id ProjectID) bool {
	if id.URL == "" {
		return true
	}
	n := 0
	for _, r := range removed {
		if r.ID.URL != "" {
			n++
		}
	}
	return n < MaxRemovedProjects
}

func sameURL(a, b ProjectID) bool {
	return a.URL != "" && b.URL != "" && sameRepository(a.URL, b.URL)
}

func sameProject(a, b ProjectID) bool {
	return a.Name == b.Name && sameURL(a, b)
}

func NextToTrim[T any](removed []RemovedProject[T]) int {
	if i := slices.IndexFunc(removed, func(r RemovedProject[T]) bool { return r.ID.URL != "" }); i >= 0 {
		return i
	}
	return 0
}

func parkProject[T any](active map[string]T, removed []RemovedProject[T], id ProjectID) ([]RemovedProject[T], []RemovedProject[T], bool) {
	data, ok := active[id.Name]
	if !ok || id.Name == "" || id.URL == "" {
		return removed, nil, false
	}
	delete(active, id.Name)
	out := slices.DeleteFunc(slices.Clone(removed), func(r RemovedProject[T]) bool { return sameProject(r.ID, id) })
	out = append(out, RemovedProject[T]{ID: id, Data: data})
	var dropped []RemovedProject[T]
	over := -MaxRemovedProjects
	for _, r := range out {
		if r.ID.URL != "" {
			over++
		}
	}
	out = slices.DeleteFunc(out, func(r RemovedProject[T]) bool {
		if over <= 0 || r.ID.URL == "" {
			return false
		}
		over--
		dropped = append(dropped, r)
		return true
	})
	return out, dropped, true
}

func setAside[T any](active map[string]T, removed []RemovedProject[T], name string) []RemovedProject[T] {
	data, ok := active[name]
	if !ok {
		return removed
	}
	delete(active, name)
	return append(slices.Clone(removed), RemovedProject[T]{ID: ProjectID{Name: name}, Data: data})
}

func restoreProject[T any](active map[string]T, removed []RemovedProject[T], id ProjectID) ([]RemovedProject[T], bool) {
	_, had := active[id.Name]
	out := setAside(active, removed, id.Name)
	i := slices.IndexFunc(out, func(r RemovedProject[T]) bool { return sameProject(r.ID, id) })
	if i < 0 {
		i = lastIndex(out, func(r RemovedProject[T]) bool { return sameURL(r.ID, id) })
	}
	if i < 0 {
		return out, had
	}
	active[id.Name] = out[i].Data
	return slices.Delete(out, i, i+1), true
}

func lastIndex[T any](list []T, match func(T) bool) int {
	for i, x := range slices.Backward(list) {
		if match(x) {
			return i
		}
	}
	return -1
}

func keepProjects[T any](active map[string]T, removed []RemovedProject[T], names []string) ([]RemovedProject[T], int) {
	var unlisted []string
	for name := range active {
		if !slices.Contains(names, name) {
			unlisted = append(unlisted, name)
		}
	}
	slices.Sort(unlisted)
	for _, name := range unlisted {
		removed = setAside(active, removed, name)
	}
	return removed, len(unlisted)
}
