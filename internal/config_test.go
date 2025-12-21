package internal_test

import (
	"testing"

	"github.com/krelinga/go-libs/deep"
	"github.com/krelinga/go-libs/exam"
	"github.com/krelinga/go-libs/match"
	"github.com/krelinga/video-transcoder/internal"
)

func TestConfig(t *testing.T) {
	e := exam.New(t)
	env := deep.NewEnv()

	e.Run("NewConfigFromEnv", func(e exam.E) {
		// Set up environment variables for the test
		exam.SetEnv(e, internal.EnvServerPort, "80")
		exam.SetEnv(e, internal.EnvDatabaseHost, "db-host")
		exam.SetEnv(e, internal.EnvDatabasePort, "5432")
		exam.SetEnv(e, internal.EnvDatabaseUser, "db-user")
		exam.SetEnv(e, internal.EnvDatabasePassword, "db-password")
		exam.SetEnv(e, internal.EnvDatabaseName, "db-name")

		tests := []struct {
			loc exam.Loc
			name string
			envVarsToSet map[string]string
			envVarsToClear []string
			wantConfig *internal.Config
			wantPanic error
		} {
			{
				loc: exam.Here(),
				name: "All environment variables set correctly",
				wantConfig: &internal.Config{
					Server: &internal.ServerConfig{
						Port: 80,
					},
					Database: &internal.DatabaseConfig{
						Host:     "db-host",
						Port:     5432,
						User:     "db-user",
						Password: "db-password",
						Name:     "db-name",
					},
				},
			},
			{
				loc: exam.Here(),
				name: "Missing VT_SERVER_PORT",
				envVarsToClear: []string{internal.EnvServerPort},
				wantPanic: internal.ErrPanicEnvNotSet,
			},
			{
				loc: exam.Here(),
				name: "Non-integer VT_SERVER_PORT",
				envVarsToSet: map[string]string{internal.EnvServerPort: "not-an-int"},
				wantPanic: internal.ErrPanicEnvNotInt,
			},
			{
				loc: exam.Here(),
				name: "Missing VT_DB_HOST",
				envVarsToClear: []string{internal.EnvDatabaseHost},
				wantPanic: internal.ErrPanicEnvNotSet,
			},
			{
				loc: exam.Here(),
				name: "Non-integer VT_DB_PORT",
				envVarsToSet: map[string]string{internal.EnvDatabasePort: "not-an-int"},
				wantPanic: internal.ErrPanicEnvNotInt,
			},
			{
				loc: exam.Here(),
				name: "Missing VT_DB_USER",
				envVarsToClear: []string{internal.EnvDatabaseUser},
				wantPanic: internal.ErrPanicEnvNotSet,
			},
			{
				loc: exam.Here(),
				name: "Missing VT_DB_PASSWORD",
				envVarsToClear: []string{internal.EnvDatabasePassword},
				wantPanic: internal.ErrPanicEnvNotSet,
			},
			{
				loc: exam.Here(),
				name: "Missing VT_DB_NAME",
				envVarsToClear: []string{internal.EnvDatabaseName},
				wantPanic: internal.ErrPanicEnvNotSet,
			},
		}
		for _, tt := range tests {
			e.Run(tt.name, func(e exam.E) {
				e.Log("Running test at", tt.loc)

				// Set additional environment variables for this test case
				for k, v := range tt.envVarsToSet {
					exam.SetEnv(e, k, v)
				}
				// Clear specified environment variables for this test case
				for _, k := range tt.envVarsToClear {
					exam.ClearEnv(e, k)
				}

				if tt.wantPanic != nil {
					exam.PanicWith(e, env, match.As[error](match.ErrorIs(tt.wantPanic)), func() {
						internal.NewConfigFromEnv()
					})
				} else {
					gotConfig := internal.NewConfigFromEnv()
					exam.Equal(e, env, tt.wantConfig, gotConfig)
				}
			})
		}
	})
}