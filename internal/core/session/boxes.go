package session

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/netip"
)

type Boxes interface {
	Create(ctx context.Context, box string) error
	Start(ctx context.Context, box string) (BoxInfo, error)
	Stop(ctx context.Context, box string) error
	Remove(ctx context.Context, box string) error
	List(ctx context.Context) ([]BoxInfo, error)
}

type BoxInfo struct {
	Box, State, Recipe, Host string
	Engine, Agent, Gateway   netip.Addr
	Subnet                   netip.Prefix
	Facts                    []string
}

var (
	ErrBoxLimit       = errors.New("the stack runs as many session containers as it is set to run at once")
	ErrBoxRuntime     = errors.New("the container runtime the stack is set to use isn't installed in Docker")
	ErrBoxUnknown     = errors.New("the session's container isn't known to the box helper")
	ErrBoxRefused     = errors.New("the box helper refused")
	ErrBoxUnavailable = errors.New("the box helper didn't answer")
)

const boxIDLength = 16

func ValidBox(s string) bool {
	if len(s) != boxIDLength {
		return false
	}
	for i := range len(s) {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func NewBox() (string, error) {
	b := make([]byte, boxIDLength/2)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
