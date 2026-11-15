package shared

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"slices"
)

const (
	RequestHeader = "X-SF-Request"
	RequestKey    = "req"
	requestBytes  = 8
)

type requestKey struct{}

func NewRequest() string {
	b := make([]byte, requestBytes)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func ValidRequest(id string) bool {
	if len(id) != 2*requestBytes {
		return false
	}
	for _, c := range id {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func WithRequest(ctx context.Context, id string) context.Context {
	if !ValidRequest(id) {
		return ctx
	}
	return context.WithValue(ctx, requestKey{}, id)
}

func RequestOf(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	id, _ := ctx.Value(requestKey{}).(string)
	return id
}

func CarryRequest(to, from context.Context) context.Context {
	return WithRequest(to, RequestOf(from))
}

func For(ctx context.Context, l Logger) Logger {
	return Tagged(l, RequestOf(ctx))
}

func Tagged(l Logger, id string) Logger {
	if l == nil || !ValidRequest(id) {
		return l
	}
	if t, ok := l.(tagged); ok {
		l = t.Logger
	}
	return tagged{Logger: l, id: id}
}

type tagged struct {
	Logger
	id string
}

func (t tagged) with(args []any) []any {
	return append(slices.Clone(args), RequestKey, t.id)
}

func (t tagged) Info(message string, args ...any)  { t.Logger.Info(message, t.with(args)...) }
func (t tagged) Warn(message string, args ...any)  { t.Logger.Warn(message, t.with(args)...) }
func (t tagged) Error(message string, args ...any) { t.Logger.Error(message, t.with(args)...) }
