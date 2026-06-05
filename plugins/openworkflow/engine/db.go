package engine

import (
	"errors"
	"time"

	"github.com/pocketbase/dbx"
)

// Engine is an OpenWorkflow Backend implementation over a dedicated SQLite
// database. It owns its own connection pools so the queue's write-heavy hot path
// is isolated from PocketBase's data.db, and it never imports a SQLite driver
// itself: the pools are opened through a ConnectFunc (see Open), mirroring
// core.DBConnect.
type Engine struct {
	readDB  *dbx.DB
	writeDB *dbx.DB
}

// ConnectFunc opens a dbx pool for the dedicated workflow DB. It is identical to
// core.DBConnectFunc, so a PocketBase app's own DBConnect (including
// core.DefaultDBConnect) can be passed straight through.
type ConnectFunc func(dbPath string) (*dbx.DB, error)

const (
	// readMaxOpenConns / readMaxIdleConns mirror PocketBase's auxiliary.db pool
	// defaults (core.DefaultAuxMaxOpenConns / DefaultAuxMaxIdleConns). The queue
	// is modeled on the aux pool, not the larger data.db pool (120/15).
	readMaxOpenConns = 20
	readMaxIdleConns = 3

	// connMaxIdleTime bounds how long an idle pooled connection is retained.
	connMaxIdleTime = 3 * time.Minute
)

// Open opens (or creates) the dedicated workflow database at dbPath and runs the
// vendored OpenWorkflow migrations. Pools are opened via connect; when connect is
// nil the built-in modernc driver is used (unavailable under the
// `no_default_driver` build tag, where a ConnectFunc must be supplied -- this
// mirrors core's DBConnect). It configures a concurrent read pool and a
// single-connection write pool, the same split PocketBase uses for its
// auxiliary database.
func Open(dbPath string, connect ConnectFunc) (*Engine, error) {
	if connect == nil {
		connect = defaultConnect
	}

	readDB, err := connect(dbPath)
	if err != nil {
		return nil, err
	}
	readDB.DB().SetMaxOpenConns(readMaxOpenConns)
	readDB.DB().SetMaxIdleConns(readMaxIdleConns)
	readDB.DB().SetConnMaxIdleTime(connMaxIdleTime)

	writeDB, err := connect(dbPath)
	if err != nil {
		readDB.Close()
		return nil, err
	}
	// Single writer: serializes all writes, which is what makes the atomic claim
	// correct without SKIP LOCKED. Every Backend mutation funnels through this one
	// process and this one connection, so concurrent workers (which reach the DB
	// only as HTTP clients) can never produce two competing DB writers. The single
	// connection is a hard correctness requirement, not a tunable.
	writeDB.DB().SetMaxOpenConns(1)
	writeDB.DB().SetMaxIdleConns(1)
	writeDB.DB().SetConnMaxIdleTime(connMaxIdleTime)

	e := &Engine{readDB: readDB, writeDB: writeDB}

	if err := e.migrate(); err != nil {
		e.Close()
		return nil, err
	}

	return e, nil
}

// Close closes both connection pools.
func (e *Engine) Close() error {
	var errs []error
	if e.writeDB != nil {
		errs = append(errs, e.writeDB.Close())
	}
	if e.readDB != nil {
		errs = append(errs, e.readDB.Close())
	}
	return errors.Join(errs...)
}

// Checkpoint runs a truncating WAL checkpoint on the write connection. Used by
// the backup hook to quiesce the dedicated DB during a snapshot.
func (e *Engine) Checkpoint() error {
	_, err := e.writeDB.NewQuery("PRAGMA wal_checkpoint(TRUNCATE)").Execute()
	return err
}

// tx runs fn inside a transaction on the single-connection write pool. The single
// connection (SetMaxOpenConns(1)) is what serializes writers and makes the
// read-then-write claim atomic: database/sql won't begin a second write
// transaction until this one commits, so no other writer in this process can
// interleave between the claim's SELECT and UPDATE. Workers reach the DB only
// through this one server process over HTTP, so they never open a competing
// writer of their own.
func (e *Engine) tx(fn func(tx *dbx.Tx) error) error {
	return e.writeDB.Transactional(fn)
}

// RunInBackupTx truncates the WAL and then holds the single write connection
// (blocking other writers) for the duration of fn. Used by the backup hook to
// quiesce the dedicated DB while pb_data is snapshotted, mirroring how
// core/base_backup.go quiesces data.db/auxiliary.db. The checkpoint runs before
// the transaction begins: a truncating checkpoint cannot complete inside an open
// transaction, and on the single-writer pool it would simply be a no-op there.
// Checkpoint errors are ignored (best-effort), matching core.
func (e *Engine) RunInBackupTx(fn func() error) error {
	_ = e.Checkpoint()
	return e.writeDB.Transactional(func(_ *dbx.Tx) error {
		return fn()
	})
}
