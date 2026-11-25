//go:build !linux

package actions

import "errors"

func undumpable() error {
	return errors.New("the run's ssh agent runs only on Linux, where it can make itself unreadable")
}
