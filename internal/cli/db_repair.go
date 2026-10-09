package cli

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"time"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"

	"github.com/spf13/cobra"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
)

// newDBRepairCmd implements `chb db-repair` — a Go-side recovery
// pass for a corrupt SQLite database. Default action: try VACUUM INTO
// to rebuild the file. When the signals table is corrupt and VACUUM
// can't read it, salvage its readable rows row-by-row and rebuild it
// cleanly. No other table can be salvaged.
//
// Always takes a backup of the original DB before touching anything.
//
//	chb db-repair                    # auto-detect, full pipeline
//	chb db-repair --table signals    # rebuild the corrupt signals table
//	chb db-repair --dry-run          # report only
func newDBRepairCmd() *cobra.Command {
	var (
		tableName string
		dryRun    bool
		noBackup  bool
	)
	cmd := &cobra.Command{
		Use:   "db-repair",
		Short: "Rebuild a corrupt SQLite DB, or salvage its signals table row by row",
		Long: `Recovery for a database with on-disk corruption (PRAGMA integrity_check
returns errors, queries fail with "database disk image is malformed").

Pipeline:
  1. Backup the DB to <path>.repair-bak-<timestamp> (skip with --no-backup).
  2. Run PRAGMA integrity_check; report status.
  3. If --table signals is set, rebuild the signals table by salvaging
     row-by-row (rowid scan with per-row error tolerance), recreating,
     reinserting. signals is the only table --table supports; any other
     name is refused before the backup is taken.
  4. Otherwise: try VACUUM INTO to a sibling file; if successful,
     replace the original.

All work is in pure Go via the modernc/sqlite driver — no shell tools
required.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDBRepair(tableName, dryRun, noBackup)
		},
	}
	cmd.Flags().StringVar(&tableName, "table", "", "rebuild only this table by row-by-row salvage; only 'signals' is supported")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "report only; don't write anything")
	cmd.Flags().BoolVar(&noBackup, "no-backup", false, "skip the backup step (NOT recommended)")
	return cmd
}

// runDBRepair resolves and backs up the database, then repairs it.
func runDBRepair(table string, dryRun, noBackup bool) error {
	path, err := prepareDBRepair(table, dryRun, noBackup)
	if err != nil {
		return err
	}
	conn, err := openRecoveryConn(path)
	if err != nil {
		return err
	}
	// vacuumInto closes this handle itself before replacing the file;
	// a second Close is harmless and keeps every other path covered.
	defer conn.Close()
	return repairOpenDB(conn, path, table, dryRun)
}

// prepareDBRepair resolves the database to repair, refuses a --table other
// than signals and, unless --dry-run or --no-backup, backs the database up.
func prepareDBRepair(table string, dryRun, noBackup bool) (string, error) {
	path, err := repairDBPath()
	if err != nil {
		return "", err
	}
	if table != "" && table != "signals" {
		return "", fmt.Errorf("--table supports only 'signals'; got %q", table)
	}
	fmt.Fprintf(os.Stderr, "db-repair: target=%s\n", path)
	return path, backupBeforeRepair(path, dryRun, noBackup)
}

// repairDBPath is the database db-repair works on, --db else
// HIVE_DB_PATH, which must exist.
func repairDBPath() (string, error) {
	path := os.Getenv("HIVE_DB_PATH")
	if dbPath != "" {
		path = dbPath
	}
	if path == "" {
		return "", fmt.Errorf("--db (or HIVE_DB_PATH) required")
	}
	if _, err := os.Stat(path); err != nil {
		return "", fmt.Errorf("DB not found: %w", err)
	}
	return path, nil
}

// backupBeforeRepair backs the database up unless --dry-run or --no-backup.
func backupBeforeRepair(path string, dryRun, noBackup bool) error {
	if noBackup || dryRun {
		return nil
	}
	bak, err := backupDatabase(path, time.Now().Unix())
	if err != nil {
		return fmt.Errorf("backup: %w", err)
	}
	fmt.Fprintf(os.Stderr, "  backup → %s\n", bak)
	return nil
}

// repairOpenDB reports the database's integrity, then rebuilds the named
// table, or by default tries VACUUM INTO to a clean sibling file and, if it
// succeeds, replaces the original.
func repairOpenDB(conn *sql.DB, path, table string, dryRun bool) error {
	if err := reportIntegrity(conn); err != nil {
		return err
	}
	if table != "" {
		return rebuildTable(conn, path, table, dryRun)
	}
	return vacuumInto(conn, path, dryRun)
}

// openRecoveryConn opens the DB with PRAGMAs that maximise read
// resilience: synchronous off, and foreign_keys off (the corrupt rows we
// salvage won't all reference valid parents). It waits up to 30 s for
// another connection's lock, as the store's connections do.
//
// It does not set journal_mode, and should not: switching a WAL database to
// DELETE checkpoints and rewrites the header of a file we have just called
// possibly corrupt, and vacuumInto below depends on WAL semantics for its
// checkpoint and sidecar removal.
func openRecoveryConn(path string) (*sql.DB, error) {
	pragmas := "_pragma=foreign_keys(0)&_pragma=synchronous(OFF)&" + db.BusyTimeoutPragma()
	conn, err := sql.Open("sqlite", fmt.Sprintf("file:%s?%s", path, pragmas))
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	conn.SetMaxOpenConns(1)
	return conn, nil
}

func reportIntegrity(conn *sql.DB) error {
	lines, err := integrityCheckLines(conn)
	if err != nil {
		return err
	}
	if integrityCheckOK(lines) {
		fmt.Fprintln(os.Stderr, "  integrity_check: ok")
		return nil
	}
	fmt.Fprintln(os.Stderr, "  integrity_check: PROBLEMS DETECTED")
	for _, l := range lines {
		fmt.Fprintf(os.Stderr, "    %s\n", l)
	}
	return nil
}

// integrityCheckLines runs PRAGMA integrity_check and returns its lines.
func integrityCheckLines(conn *sql.DB) ([]string, error) {
	rows, err := conn.Query(`PRAGMA integrity_check`)
	if err != nil {
		return nil, fmt.Errorf("integrity_check: %w", err)
	}
	defer rows.Close()
	lines, err := scanIntegrityLines(rows)
	if err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		// Do not let a truncated integrity_check read like a clean one.
		return nil, fmt.Errorf("integrity_check did not complete: %w", err)
	}
	return lines, nil
}

// scanIntegrityLines reads the one text column of each integrity_check row.
// The caller checks rows.Err, which says whether the read completed.
func scanIntegrityLines(rows *sql.Rows) ([]string, error) {
	var lines []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		lines = append(lines, s)
	}
	return lines, nil
}

// integrityCheckOK reports whether integrity_check found nothing wrong: one
// line, "ok".
func integrityCheckOK(lines []string) bool {
	return len(lines) == 1 && lines[0] == "ok"
}

// vacuumInto rebuilds the DB to a sibling file via VACUUM INTO. If
// SQLite can read every page it copies the rebuilt file over the
// original. On a partially-corrupt DB this often heals minor
// corruption (free-page chain issues, btree imbalance). Severe table
// corruption may still fail — falls back with a clear message.
func vacuumInto(conn *sql.DB, path string, dryRun bool) error {
	dest := path + ".vacuum-tmp"
	fmt.Fprintf(os.Stderr, "  trying VACUUM INTO %s …\n", dest)
	// A dry run reports; it does not write, and it does not delete. A
	// pre-existing .vacuum-tmp is exactly the salvage output someone would
	// be inspecting when they reach for a dry run, so the os.Remove below
	// comes after this guard.
	if dryRun {
		fmt.Fprintln(os.Stderr, "  [dry-run] would VACUUM INTO + replace")
		return nil
	}
	if err := vacuumToSibling(conn, dest); err != nil {
		return err
	}
	if err := releaseForReplace(conn, dest); err != nil {
		return err
	}
	return replaceWithVacuumed(dest, path)
}

// vacuumToSibling writes a rebuilt copy of the database to dest, removing a
// stale copy first and a partial one on failure.
func vacuumToSibling(conn *sql.DB, dest string) error {
	_ = os.Remove(dest)
	if _, err := conn.Exec(`VACUUM INTO ?`, dest); err != nil {
		_ = os.Remove(dest)
		return fmt.Errorf("VACUUM INTO failed (if the signals table is the corrupt one, --table signals salvages it row by row; no other table can be salvaged): %w", err)
	}
	return nil
}

// releaseForReplace readies the database file to be swapped, removing dest
// when it cannot. Replacing the file underneath a live SQLite connection
// leaves every open handle — ours included — pointed at the old inode, and
// deleting a live -wal discards committed transactions that have not been
// checkpointed. So: take the exclusive lock, which fails plainly if another
// process has the database open; checkpoint; close our own handle; only
// then swap.
func releaseForReplace(conn *sql.DB, dest string) error {
	if err := lockExclusive(conn); err != nil {
		_ = os.Remove(dest)
		return err
	}
	if _, err := conn.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		fmt.Fprintf(os.Stderr, "  checkpoint before replace: %v\n", err)
	}
	if err := conn.Close(); err != nil {
		_ = os.Remove(dest)
		return fmt.Errorf("close the database before replacing it: %w", err)
	}
	return nil
}

// replaceWithVacuumed moves the rebuilt copy over the original and removes
// the original's WAL sidecars.
func replaceWithVacuumed(dest, path string) error {
	if err := os.Rename(dest, path); err != nil {
		return fmt.Errorf("replace original: %w", err)
	}
	// Safe now: nothing holds the database, and the checkpoint above folded
	// the WAL back into the file these sidecars no longer describe.
	_ = os.Remove(path + "-wal")
	_ = os.Remove(path + "-shm")
	fmt.Fprintln(os.Stderr, "  VACUUM INTO succeeded; original replaced.")
	return nil
}

// lockExclusive claims the database for this process alone. SQLite holds an
// EXCLUSIVE lock from the first write until the connection closes, so a second
// process using the database makes this fail here — loudly, and before
// anything has been replaced — instead of silently ending up on an orphaned
// inode afterwards.
func lockExclusive(conn *sql.DB) error {
	if _, err := conn.Exec(`PRAGMA locking_mode = EXCLUSIVE`); err != nil {
		return fmt.Errorf("claim the database: %w", err)
	}
	if _, err := conn.Exec(`BEGIN IMMEDIATE`); err != nil {
		return fmt.Errorf("the database is in use by another process, so it cannot be replaced safely: %w", err)
	}
	if _, err := conn.Exec(`COMMIT`); err != nil {
		return fmt.Errorf("release the write lock: %w", err)
	}
	return nil
}

// rebuildTable salvages every readable row from the named table by
// scanning rowid-by-rowid with per-row error tolerance. Drops the
// corrupt table, recreates it from the schema initialiser, and
// reinserts the salvaged rows.
//
// Specialised for `signals`, the table that corrupts in practice, and the
// command refuses any other name before it gets here. Generalising it means
// reading the column list from PRAGMA table_info(<table>).
func rebuildTable(conn *sql.DB, path, table string, dryRun bool) error {
	fmt.Fprintf(os.Stderr, "  salvaging rows from %s …\n", table)

	salvaged, lostRowids, err := salvageSignalsRows(conn)
	if err != nil {
		return fmt.Errorf("salvage: %w", err)
	}
	fmt.Fprintf(os.Stderr, "  salvaged %d rows; %d unreadable rowids\n",
		len(salvaged), len(lostRowids))

	if dryRun {
		fmt.Fprintf(os.Stderr, "  [dry-run] would drop+recreate %s and reinsert %d rows\n",
			table, len(salvaged))
		return nil
	}
	if err := recreateSignalsTable(conn, path, salvaged); err != nil {
		return err
	}
	reportRebuiltTable(table, len(salvaged), lostRowids)
	return nil
}

// recreateSignalsTable drops the signals table, creates it afresh and reinserts
// the salvaged rows. runDBRepair closes conn; a connection the
// writable_schema bypass opened in its place is closed here, on every path.
func recreateSignalsTable(conn *sql.DB, path string, salvaged []signalRow) error {
	live, err := dropSignalsTable(conn, path)
	if err != nil {
		return err
	}
	if live != conn {
		defer live.Close()
	}
	if err := createSignalsTable(live); err != nil {
		return err
	}
	return reinsertSalvagedSignals(live, salvaged)
}

// dropSignalsTable drops the signals table and returns the connection to go
// on with; createSignalsTable then recreates it as the schema does. On a
// corrupt btree, the standard DROP TABLE can't read the page index. Fall
// back to writable_schema: edit sqlite_master directly to forget the table,
// leaving orphaned pages on disk (cleaned up by a subsequent VACUUM).
// Documented SQLite recovery pattern.
func dropSignalsTable(conn *sql.DB, path string) (*sql.DB, error) {
	_, err := conn.Exec(`DROP TABLE IF EXISTS signals`)
	if err == nil {
		return conn, nil
	}
	if !isCorruptionError(err) {
		return nil, fmt.Errorf("drop signals: %w", err)
	}
	fmt.Fprintf(os.Stderr, "  DROP TABLE failed (corruption); using writable_schema bypass\n")
	return forgetSignalsTable(conn, path)
}

// forgetSignalsTable removes the signals table and its indexes from
// sqlite_master, then closes conn and returns a fresh connection that sees
// the deletion. The caller closes that connection.
func forgetSignalsTable(conn *sql.DB, path string) (*sql.DB, error) {
	if err := runSignalsBypass(conn); err != nil {
		return nil, err
	}
	// SQLite caches sqlite_master per-connection. Close + reopen
	// so the CREATE TABLE below sees the deletion.
	_ = conn.Close()
	reopened, err := openRecoveryConn(path)
	if err != nil {
		return nil, fmt.Errorf("reopen after bypass: %w", err)
	}
	return reopened, nil
}

// runSignalsBypass deletes the signals table and its indexes from
// sqlite_master and bumps schema_version, in one transaction with
// writable_schema on around it, all on one connection. A failure part-way
// rolls back to the table as it was, corrupt but present; run one by one, a
// failure after the first DELETE would leave the table dropped and its
// indexes naming a table that does not exist. A PRAGMA takes a literal
// value, not an expression, so the version is read first.
func runSignalsBypass(conn *sql.DB) error {
	ctx := context.Background()
	c, err := conn.Conn(ctx)
	if err != nil {
		return fmt.Errorf("writable_schema bypass: pin connection: %w", err)
	}
	defer c.Close()
	var version int64
	if err := c.QueryRowContext(ctx, `PRAGMA schema_version`).Scan(&version); err != nil {
		return fmt.Errorf("read schema_version: %w", err)
	}
	return withWritableSchema(ctx, c, func() error { return execInTx(ctx, c, signalsForget(version)) })
}

// signalsForget forgets the signals table and its indexes, and bumps
// schema_version so a connection opened afterwards sees the deletion.
func signalsForget(version int64) []string {
	return []string{
		`DELETE FROM sqlite_master WHERE name = 'signals'`,
		`DELETE FROM sqlite_master WHERE name LIKE 'idx_signals_%'`,
		fmt.Sprintf(`PRAGMA schema_version = %d`, version+1),
	}
}

// withWritableSchema runs fn on c with writable_schema on, and turns it off
// afterwards, whatever fn returned.
func withWritableSchema(ctx context.Context, c *sql.Conn, fn func() error) error {
	if _, err := c.ExecContext(ctx, `PRAGMA writable_schema = 1`); err != nil {
		return fmt.Errorf("writable_schema bypass: %w", err)
	}
	err := fn()
	if _, offErr := c.ExecContext(ctx, `PRAGMA writable_schema = 0`); err == nil && offErr != nil {
		return fmt.Errorf("writable_schema bypass: %w", offErr)
	}
	return err
}

// execInTx runs stmts on c in one transaction, rolled back at the first
// that fails.
func execInTx(ctx context.Context, c *sql.Conn, stmts []string) error {
	tx, err := c.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("writable_schema bypass: begin: %w", err)
	}
	for _, s := range stmts {
		if _, err := tx.ExecContext(ctx, s); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("writable_schema bypass %q: %w", s, err)
		}
	}
	return tx.Commit()
}

// createSignalsTable creates the signals table and its indexes with the
// schema's own statements (db.SignalsSchema), so the rebuilt table is the
// one a new store has.
func createSignalsTable(conn *sql.DB) error {
	stmts := slices.Concat([]string{`PRAGMA foreign_keys = OFF`}, db.SignalsSchema(), []string{`PRAGMA foreign_keys = ON`})
	for _, s := range stmts {
		if _, err := conn.Exec(s); err != nil {
			return fmt.Errorf("rebuild %q: %w", repairStmtSnippet(s), err)
		}
	}
	return nil
}

// repairStmtSnippet is a statement cut to its first 60 bytes, for an error
// message.
func repairStmtSnippet(s string) string {
	if len(s) > 60 {
		return clip(s, 60) + "…"
	}
	return s
}

// reinsertSalvagedSignals inserts the salvaged rows in one transaction,
// rolled back on any failure.
func reinsertSalvagedSignals(conn *sql.DB, salvaged []signalRow) error {
	tx, err := conn.BeginTx(context.Background(), nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	if err := insertSalvagedSignals(tx, salvaged); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

// insertSalvagedSignals inserts the rows, ids included, through tx.
func insertSalvagedSignals(tx *sql.Tx, salvaged []signalRow) error {
	stmt, err := tx.Prepare(
		`INSERT INTO signals
		 (id, signal_type, source_type, source_id,
		  target_d1, target_d2, target_d3, target_d4,
		  payload_json, acted_on, wave, created_at)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
	)
	if err != nil {
		return fmt.Errorf("prepare insert: %w", err)
	}
	defer stmt.Close()
	for _, r := range salvaged {
		if _, err := stmt.Exec(
			r.id, r.signalType, r.sourceType, r.sourceID,
			r.d1, r.d2, r.d3, r.d4,
			r.payload, r.actedOn, r.wave, r.createdAt,
		); err != nil {
			return fmt.Errorf("reinsert rowid %d: %w", r.id, err)
		}
	}
	return nil
}

// reportRebuiltTable says the table was rebuilt and warns of the rowids
// salvage could not read, listing up to ten.
func reportRebuiltTable(table string, rebuilt int, lostRowids []int64) {
	fmt.Fprintf(os.Stderr, "  rebuilt %s with %d rows\n", table, rebuilt)
	if len(lostRowids) > 0 {
		fmt.Fprintf(os.Stderr, "  WARNING: %d rowids were unreadable and have been lost: %v\n",
			len(lostRowids), lostRowids[:min(len(lostRowids), 10)])
	}
}

type signalRow struct {
	id             int64
	signalType     string
	sourceType     sql.NullString
	sourceID       sql.NullInt64
	d1, d2, d3, d4 sql.NullInt64
	payload        string
	actedOn        int
	wave           sql.NullInt64
	createdAt      string
}

// salvageSignalsRows streams every row from the signals table in
// rowid order, scanning each row independently. A scan failure on one
// row does not stop the iteration — that row is recorded as lost and
// the scan continues. Single SELECT, O(rows) round-trips — vs. the
// earlier point-lookup version which made one round-trip per rowid
// (O(MAX(rowid)) — pathological on sparse rowids).
//
// When the SELECT itself can't open (e.g. the btree root page is
// corrupt), salvage returns zero rows + a sentinel "lost" entry so
// the caller knows the table is irrecoverable and can fall through
// to the writable_schema bypass.
func salvageSignalsRows(conn *sql.DB) ([]signalRow, []int64, error) {
	rows, err := conn.Query(
		`SELECT rowid, id, signal_type, source_type, source_id,
		        target_d1, target_d2, target_d3, target_d4,
		        payload_json, acted_on, wave, created_at
		 FROM signals ORDER BY rowid`,
	)
	if err != nil {
		// Table-level read failure — nothing to salvage. Sentinel
		// "lost" rowid 0 signals the unrecoverable state.
		return nil, []int64{0}, nil
	}
	defer rows.Close()

	salvaged, lost := scanSalvagedSignals(rows)
	if err := rows.Err(); err != nil {
		// Iteration aborted mid-stream — the rest of the table is
		// unreadable. Caller still uses what we got.
		fmt.Fprintf(os.Stderr, "  salvage iteration ended early: %v\n", err)
	}
	return salvaged, lost, nil
}

// scanSalvagedSignals reads each row on its own: one that does not scan is
// recorded by rowid as lost, and the scan goes on.
func scanSalvagedSignals(rows *sql.Rows) ([]signalRow, []int64) {
	var (
		salvaged []signalRow
		lost     []int64
	)
	for rows.Next() {
		var (
			rowid int64
			r     signalRow
		)
		if err := rows.Scan(
			&rowid,
			&r.id, &r.signalType, &r.sourceType, &r.sourceID,
			&r.d1, &r.d2, &r.d3, &r.d4,
			&r.payload, &r.actedOn, &r.wave, &r.createdAt,
		); err != nil {
			lost = append(lost, rowid)
			continue
		}
		salvaged = append(salvaged, r)
	}
	return salvaged, lost
}

// backupDatabase copies the main file to <path>.repair-bak-<stamp> and,
// when present, the -wal and -shm sidecars under the matching names. In WAL
// mode the newest committed pages live only in the WAL until a checkpoint,
// so a copy of the main file alone silently drops them; SQLite reattaches
// sidecars by filename, so the trio restores as a unit.
func backupDatabase(path string, stamp int64) (string, error) {
	bak := fmt.Sprintf("%s.repair-bak-%d", path, stamp)
	if err := copyFile(path, bak); err != nil {
		return "", err
	}
	for _, sidecar := range []string{"-wal", "-shm"} {
		if err := copyDBSidecar(path, bak, sidecar); err != nil {
			return "", err
		}
	}
	return bak, nil
}

// copyDBSidecar copies the database's sidecar file beside the backup, and
// does nothing when the database has none.
func copyDBSidecar(path, bak, sidecar string) error {
	if _, err := os.Stat(path + sidecar); err != nil {
		return nil
	}
	if err := copyFile(path+sidecar, bak+sidecar); err != nil {
		return fmt.Errorf("%s sidecar: %w", sidecar, err)
	}
	return nil
}

// copyFile is a streaming file copy. Used for the pre-repair backup.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	return writeBackupCopy(dst, in)
}

// writeBackupCopy creates dst, copies r into it and syncs it to disk.
func writeBackupCopy(dst string, r io.Reader) error {
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, r); err != nil {
		return err
	}
	return out.Sync()
}

// isCorruptionError reports whether err is SQLite's report of a damaged
// file, by its result code: SQLITE_CORRUPT ("database disk image is
// malformed") or SQLITE_NOTADB ("file is not a database"), extended codes
// included.
func isCorruptionError(err error) bool {
	var se *sqlite.Error
	if !errors.As(err, &se) {
		return false
	}
	code := se.Code() & 0xff
	return code == sqlite3.SQLITE_CORRUPT || code == sqlite3.SQLITE_NOTADB
}
