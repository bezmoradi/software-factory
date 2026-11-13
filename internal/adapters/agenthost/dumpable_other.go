//go:build !linux

package agenthost

func ProtectMemory() error { return nil }
