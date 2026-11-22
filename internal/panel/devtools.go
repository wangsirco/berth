//go:build devtools

package panel

import "database/sql"

// DevUser adds (or with del, removes) a super admin: for end-to-end tests on a dev machine
// only. Built with -tags devtools, never part of a release.
func DevUser(db *sql.DB, name, password string, del bool) error {
	if del {
		_, err := db.Exec(`DELETE FROM users WHERE username=?`, name)
		return err
	}
	a := &App{db: db}
	_, err := a.createUser(name, name, password, RoleSuper)
	return err
}
