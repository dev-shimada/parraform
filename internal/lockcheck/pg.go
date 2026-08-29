package lockcheck

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/dev-shimada/parraform/internal/backendcfg"
)

func init() {
	Register("pg", pgChecker{})
}

// pgChecker peeks the pg (Postgres) backend's lock via a non-blocking
// pg_try_advisory_lock immediately followed by pg_advisory_unlock if
// acquired -- the same "attempt and release" pattern as the local
// backend's flock peek, since Postgres advisory locks have no read-only
// "is it locked" query as clean as the object-existence checks the other
// backends offer. Both calls are pinned to a single *sql.Conn (session
// locks are per-connection, not per-query) so nothing leaks if we do
// acquire it.
//
// Verified against terraform's own pg backend source
// (client.go/backend.go/backend_state.go): the lock key is not a hash,
// it's the "id" column of the row in "<schema_name>.states" matching the
// workspace name (schema defaults to "terraform_remote_state", table name
// "states" is a hardcoded constant); the workspace name has no
// default-workspace special case (used literally, like GCS/kubernetes);
// and lock info (the Who field) is kept only in the locking process's
// memory, never persisted anywhere readable -- so Info.Who is always
// empty for this backend, which is expected, not a bug.
//
// IMPORTANT: unlike every other checker in this package, this one has not
// been exercised against a real (or faked) Postgres server -- the wire
// protocol isn't practically fakeable with httptest the way the HTTP-based
// backends were, and docker access to run a real postgres for integration
// testing was not available while building this. Only the pure
// identifier-quoting/defaulting logic below is unit tested. Treat this
// checker as lower-confidence than the others until it's been exercised
// against a real database.
type pgChecker struct{}

func (pgChecker) Peek(ctx context.Context, cfg backendcfg.Config) (Info, bool, error) {
	// conn_str and schema_name both fall back to PG_CONN_STR/PG_SCHEMA_NAME
	// env vars in terraform's own pg backend (verified from backend.go's
	// Configure(), via backendbase.NewSDKLikeData). That resolution
	// happens inside terraform at Configure() time, not before the config
	// is cached to .terraform/terraform.tfstate, so a config that omits
	// these attributes in favor of the env var (the common CI pattern)
	// would otherwise silently look unconfigured here.
	connStr, _ := cfg.Config["conn_str"].(string)
	if connStr == "" {
		connStr = os.Getenv("PG_CONN_STR")
	}
	if connStr == "" {
		return Info{}, false, nil
	}
	schema, _ := cfg.Config["schema_name"].(string)
	if schema == "" {
		schema = os.Getenv("PG_SCHEMA_NAME")
	}
	if schema == "" {
		schema = "terraform_remote_state"
	}
	workspace := cfg.Workspace
	if workspace == "" {
		workspace = "default"
	}

	db, err := sql.Open("pgx", connStr)
	if err != nil {
		return Info{}, false, fmt.Errorf("opening postgres connection: %w", err)
	}
	defer db.Close()

	return peekPgAdvisoryLock(ctx, db, schema, workspace)
}

// pgQuoteIdent quotes a Postgres identifier defensively (schema_name comes
// from backend config, not user input at request time, but this avoids
// depending on it never containing special characters).
func pgQuoteIdent(ident string) string {
	return `"` + strings.ReplaceAll(ident, `"`, `""`) + `"`
}

func peekPgAdvisoryLock(ctx context.Context, db *sql.DB, schema, workspace string) (Info, bool, error) {
	conn, err := db.Conn(ctx)
	if err != nil {
		return Info{}, false, err
	}
	defer conn.Close()

	query := fmt.Sprintf(`SELECT id FROM %s.states WHERE name = $1`, pgQuoteIdent(schema))
	var id int64
	err = conn.QueryRowContext(ctx, query, workspace).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		// No row for this workspace yet: state was never written, so
		// there's nothing to lock.
		return Info{Locked: false}, true, nil
	}
	if err != nil {
		return Info{}, false, err
	}

	var acquired bool
	if err := conn.QueryRowContext(ctx, `SELECT pg_try_advisory_lock($1)`, id).Scan(&acquired); err != nil {
		return Info{}, false, err
	}
	if !acquired {
		return Info{Locked: true}, true, nil
	}

	if _, err := conn.ExecContext(ctx, `SELECT pg_advisory_unlock($1)`, id); err != nil {
		return Info{Locked: false}, true, fmt.Errorf("releasing advisory lock after peek: %w", err)
	}
	return Info{Locked: false}, true, nil
}
