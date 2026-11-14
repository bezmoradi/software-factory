package main

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const expiryLayout = "2006-01-02 15:04:05 MST"

func expiryPath(root string) string { return filepath.Join(root, "token-expiry") }

func tokenExpiry(root string) string {
	data, err := os.ReadFile(expiryPath(root))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func expiryCommand(root string, args []string) error {
	if len(args) != 2 {
		return errors.New("usage: sf-gitserver token-expiry <days from now>|off")
	}
	if args[1] == "off" {
		if err := os.Remove(expiryPath(root)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	days, err := strconv.ParseFloat(args[1], 64)
	if err != nil || days <= 0 || days > 3650 {
		return errors.New(args[1] + " isn't a number of days from 0 to 3650")
	}
	at := time.Now().UTC().Add(time.Duration(days * float64(24*time.Hour))).Format(expiryLayout)
	return os.WriteFile(expiryPath(root), []byte(at+"\n"), 0o600)
}
