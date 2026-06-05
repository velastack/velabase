//go:build !no_default_driver

package engine

import (
	"github.com/pocketbase/dbx"
	_ "modernc.org/sqlite"
)

// basePragmas is kept byte-for-byte in sync with core.DefaultDBConnect
// (core/db_connect.go). busy_timeout must come first (per core's note) so the
// connection blocks on busy before WAL mode is negotiated.
const basePragmas = "?_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)&_pragma=journal_size_limit(200000000)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(ON)&_pragma=temp_store(MEMORY)&_pragma=cache_size(-32000)"

// defaultConnect is the built-in modernc-based ConnectFunc, used when Open's
// connect argument is nil. It matches core.DefaultDBConnect (same driver, same
// pragmas). No BEGIN IMMEDIATE / _txlock variant is needed: the atomic-claim
// guarantee comes from the single-connection write pool (see Engine.tx), not from
// any txlock pragma.
func defaultConnect(dbPath string) (*dbx.DB, error) {
	return dbx.Open("sqlite", dbPath+basePragmas)
}
