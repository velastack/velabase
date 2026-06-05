// Package openworkflow registers an OpenWorkflow-compatible durable workflow
// engine into a PocketBase app: a dedicated SQLite database (1:1 with the
// reference SQLite backend) plus an HTTP API that mirrors OpenWorkflow's
// Backend interface. External OpenWorkflow workers drive it over HTTP via the
// separate pocketbase-js BackendPocketBase implementation.
package openworkflow

import (
	"path/filepath"

	"github.com/pocketbase/pocketbase/apis"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/plugins/openworkflow/engine"
)

// Config holds the plugin options.
type Config struct {
	// DBFilename is the dedicated workflow database file name under pb_data.
	// Defaults to "openworkflow.db".
	DBFilename string

	// DefaultNamespace is advertised to clients; it does not restrict requests.
	// Defaults to "default".
	DefaultNamespace string

	// AllowedNamespaces optionally restricts which namespaces requests may
	// target. Empty (default) allows any namespace (single-tenant trust model).
	AllowedNamespaces []string

	// RoutePrefix is the base path for the HTTP API. Defaults to "/api/ow/v1".
	RoutePrefix string

	// DBConnect overrides how the dedicated workflow database pools are opened.
	// Leave nil to use the built-in modernc driver (same as core's default). When
	// building with the `no_default_driver` tag this MUST be set, mirroring the
	// app's own DBConnect config option.
	DBConnect core.DBConnectFunc
}

type plugin struct {
	app    core.App
	config Config
	engine *engine.Engine
}

// MustRegister is like Register but panics on error.
func MustRegister(app core.App, config Config) {
	if err := Register(app, config); err != nil {
		panic(err)
	}
}

// Register wires the OpenWorkflow engine into the app: it opens the dedicated
// database on bootstrap, registers the HTTP API on serve, quiesces the DB
// during backups, and closes it on terminate.
func Register(app core.App, config Config) error {
	if config.DBFilename == "" {
		config.DBFilename = "openworkflow.db"
	}
	if config.DefaultNamespace == "" {
		config.DefaultNamespace = "default"
	}
	if config.RoutePrefix == "" {
		config.RoutePrefix = "/api/ow/v1"
	}

	p := &plugin{app: app, config: config}

	// Open the engine after core bootstrap (pb_data exists by then).
	app.OnBootstrap().BindFunc(func(e *core.BootstrapEvent) error {
		if err := e.Next(); err != nil {
			return err
		}
		eng, err := engine.Open(filepath.Join(app.DataDir(), config.DBFilename), engine.ConnectFunc(config.DBConnect))
		if err != nil {
			return err
		}
		p.engine = eng
		return nil
	})

	// Register the HTTP API.
	app.OnServe().BindFunc(func(se *core.ServeEvent) error {
		p.registerRoutes(se)
		return se.Next()
	})

	// Quiesce the dedicated DB for the duration of a backup snapshot so the
	// file captured in the pb_data archive is consistent.
	app.OnBackupCreate().BindFunc(func(e *core.BackupEvent) error {
		if p.engine == nil {
			return e.Next()
		}
		return p.engine.RunInBackupTx(func() error {
			return e.Next()
		})
	})

	// Close pools on shutdown.
	app.OnTerminate().BindFunc(func(e *core.TerminateEvent) error {
		if p.engine != nil {
			_ = p.engine.Close()
		}
		return e.Next()
	})

	return nil
}

// checkNamespace validates the path namespace against the optional allowlist.
func (p *plugin) checkNamespace(ns string) error {
	if ns == "" {
		return apis.NewBadRequestError("missing namespace", nil)
	}
	if len(p.config.AllowedNamespaces) == 0 {
		return nil
	}
	for _, allowed := range p.config.AllowedNamespaces {
		if allowed == ns {
			return nil
		}
	}
	return apis.NewForbiddenError("namespace not allowed", nil)
}
