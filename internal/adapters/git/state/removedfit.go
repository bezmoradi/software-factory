package state

import (
	"encoding/json"
	"slices"

	"github.com/bezmoradi/software-factory/internal/core/session"
)

func fitted[T, J any](removed []session.RemovedProject[T], toJSON func(session.RemovedProject[T]) J, encodeWith func([]J) ([]byte, error), records func(T) int) ([]byte, int, error) {
	removed = slices.Clone(removed)
	list := make([]J, 0, len(removed))
	for _, r := range removed {
		list = append(list, toJSON(r))
	}
	dropped := 0
	for {
		data, err := encodeWith(list)
		if err != nil || len(data) <= maxFile || len(removed) == 0 {
			return data, dropped, err
		}
		excess := len(data) - maxFile
		for freed := 0; freed < excess && len(removed) > 0; {
			i := session.NextToTrim(removed)
			b, err := json.Marshal(list[i])
			if err != nil {
				return nil, dropped, err
			}
			freed += len(b)
			dropped += records(removed[i].Data)
			removed, list = slices.Delete(removed, i, i+1), slices.Delete(list, i, i+1)
		}
	}
}
