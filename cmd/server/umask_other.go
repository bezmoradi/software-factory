//go:build !unix

package server

func ApplyUmask(func(string) string) error { return nil }
