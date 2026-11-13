package slog

import (
	"github.com/bezmoradi/software-factory/internal/core/shared"
	"log/slog"
	"os"
)

type logger struct {
	logger *slog.Logger
}

func New(fileName ...string) shared.Logger {
	if len(fileName) > 0 {
		file, err := os.OpenFile(fileName[0], os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
		if err != nil {
			panic(err)
		}
		return logger{
			logger: slog.New(slog.NewTextHandler(file, nil)),
		}
	}

	return logger{
		logger: slog.New(slog.NewTextHandler(os.Stdout, nil)),
	}
}

func (l logger) Info(message string, args ...any) {
	l.logger.Info(message, args...)
}

func (l logger) Warn(message string, args ...any) {
	l.logger.Warn(message, args...)
}

func (l logger) Error(message string, args ...any) {
	l.logger.Error(message, args...)
}

func (l logger) Fatal(err error) {
	l.logger.Error(err.Error())
	os.Exit(1)
}
