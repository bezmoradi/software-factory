package configs

import (
	"path/filepath"

	"github.com/bezmoradi/software-factory/tools"

	"github.com/joho/godotenv"
)

func LoadEnv() {
	if rootDir, err := tools.FindRootDir(); err == nil {
		_ = godotenv.Load(filepath.Join(rootDir, ".env"))
	}
	_ = godotenv.Load()
}
