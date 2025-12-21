package internal

import (
	"errors"
	"fmt"
	"os"
	"strconv"
)

var (
	ErrPanicEnvNotSet = errors.New("environment variable not set")
	ErrPanicEnvNotInt = errors.New("environment variable is not an integer")
)

const (
	EnvServerPort       = "VT_SERVER_PORT"
	EnvDatabaseHost     = "VT_DB_HOST"
	EnvDatabasePort     = "VT_DB_PORT"
	EnvDatabaseUser     = "VT_DB_USER"
	EnvDatabasePassword = "VT_DB_PASSWORD"
	EnvDatabaseName     = "VT_DB_NAME"
)

type Config struct {
	Server   *ServerConfig
	Database *DatabaseConfig
}

type ServerConfig struct {
	Port int
}

type DatabaseConfig struct {
	Host     string
	Port     int
	User     string
	Password string
	Name     string
}

func mustGetenv(key string) string {
	value, ok := os.LookupEnv(key)
	if !ok {
		panic(fmt.Errorf("%w: %q", ErrPanicEnvNotSet, key))
	}
	return value
}

func mustGetenvAtoi(key string) int {
	valueStr := mustGetenv(key)
	value, err := strconv.Atoi(valueStr)
	if err != nil {
		panic(fmt.Errorf("%w: %q", ErrPanicEnvNotInt, key))
	}
	return value
}

func NewConfigFromEnv() *Config {
	return &Config{
		Server: &ServerConfig{
			Port: mustGetenvAtoi(EnvServerPort),
		},
		Database: &DatabaseConfig{
			Host:     mustGetenv(EnvDatabaseHost),
			Port:     mustGetenvAtoi(EnvDatabasePort),
			User:     mustGetenv(EnvDatabaseUser),
			Password: mustGetenv(EnvDatabasePassword),
			Name:     mustGetenv(EnvDatabaseName),
		},
	}
}
