//go:build no_default_driver

package engine

import "github.com/pocketbase/dbx"

// defaultConnect panics because no SQLite driver is compiled in under the
// no_default_driver build tag. Callers must supply a ConnectFunc to Open (wired
// from the PocketBase app's own DBConnect via the plugin's Config.DBConnect),
// mirroring core.DefaultDBConnect under the same tag.
func defaultConnect(dbPath string) (*dbx.DB, error) {
	panic("openworkflow: a ConnectFunc must be supplied when the no_default_driver build tag is used!")
}
