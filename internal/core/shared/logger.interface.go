package shared

type Logger interface {
	Error(message string, args ...any)
	Fatal(err error)
	Info(message string, args ...any)
	Warn(message string, args ...any)
}
