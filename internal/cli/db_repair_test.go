package cli

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
)

// --table salvages only signals. Any other name is refused before the backup
// is written, so a repair that never runs leaves nothing behind.
func TestDBRepair_TableOtherThanSignalsRefusedBeforeBackup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hive.db")
	s, err := db.NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Init(); err != nil {
		t.Fatal(err)
	}
	s.Close()
	before, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	prev := dbPath
	dbPath = path
	t.Cleanup(func() { dbPath = prev })

	cmd := newDBRepairCmd()
	cmd.SetArgs([]string{"--table", "findings"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err = cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "'signals'") {
		t.Fatalf("db-repair --table findings: err = %v; want a refusal naming signals", err)
	}
	after, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		var names []string
		for _, e := range after {
			names = append(names, e.Name())
		}
		t.Errorf("the refused repair wrote files: %v (had %d before)", names, len(before))
	}
}

// The pre-repair backup copied only the main .db file. In WAL mode the rows
// written since the last checkpoint exist only in <db>-wal, so restoring that
// backup lost them. The sidecars must travel with the main file.
func TestBackupDatabase_CarriesWALSidecars(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hive.db")
	s, err := db.NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Init(); err != nil {
		t.Fatal(err)
	}
	// A write that stays in the WAL: no checkpoint, store still open.
	if _, err := s.WriteDB.Exec(`INSERT INTO dimensions(name, description, values_json) VALUES ('d_wal', 'in the wal', '[]')`); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path + "-wal"); err != nil {
		t.Fatalf("expected a WAL sidecar while the store is open: %v", err)
	}

	bak, err := backupDatabase(path, 42)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{bak, bak + "-wal", bak + "-shm"} {
		if _, err := os.Stat(f); err != nil {
			t.Errorf("backup missing %s: %v", filepath.Base(f), err)
		}
	}
	s.Close()

	// Restoring the trio yields the WAL-only row.
	restored, err := sql.Open("sqlite", bak)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	var n int
	if err := restored.QueryRow(`SELECT COUNT(*) FROM dimensions WHERE name='d_wal'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("restored backup lost the WAL-resident row (count=%d)", n)
	}
}

// The recovery connection waits for another connection's lock, as the
// store's connections do, rather than failing with SQLITE_BUSY at once.
func TestOpenRecoveryConn_WaitsForALock(t *testing.T) {
	conn, err := openRecoveryConn(filepath.Join(t.TempDir(), "hive.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	var ms int
	if err := conn.QueryRow(`PRAGMA busy_timeout`).Scan(&ms); err != nil {
		t.Fatal(err)
	}
	if ms != 30000 {
		t.Errorf("busy_timeout is %d ms, want the store's 30000", ms)
	}
}

// The recovery connection waits as long as the store does, the
// HIVE_SQLITE_BUSY_TIMEOUT_MS override included.
func TestOpenRecoveryConn_HonoursTheTimeoutOverride(t *testing.T) {
	t.Setenv("HIVE_SQLITE_BUSY_TIMEOUT_MS", "1234")
	conn, err := openRecoveryConn(filepath.Join(t.TempDir(), "hive.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	var ms int
	if err := conn.QueryRow(`PRAGMA busy_timeout`).Scan(&ms); err != nil {
		t.Fatal(err)
	}
	if ms != 1234 {
		t.Errorf("busy_timeout is %d ms, want the override's 1234", ms)
	}
}

// --table signals on a signals table DROP TABLE cannot read goes through the
// writable_schema bypass, which closes the recovery connection and opens a
// fresh one so CREATE TABLE sees the deletion, then closes that one too.
// Once the repair returns, nothing holds the database, so a connection can
// take it exclusively.
func TestDBRepair_SignalsBypassClosesItsConnection(t *testing.T) {
	path := corruptSignalsDB(t)
	prev := dbPath
	dbPath = path
	t.Cleanup(func() { dbPath = prev })

	cmd := newDBRepairCmd()
	cmd.SetArgs([]string{"--table", "signals", "--no-backup"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	stderr, err := captureStderr(t, cmd.Execute)
	if err != nil {
		t.Fatalf("db-repair --table signals: %v\n%s", err, stderr)
	}
	if !strings.Contains(stderr, "writable_schema bypass") {
		t.Fatalf("the repair did not take the writable_schema bypass, so it tests nothing:\n%s", stderr)
	}

	conn, err := sql.Open("sqlite", "file:"+path+"?_pragma=locking_mode(EXCLUSIVE)&_pragma=busy_timeout(0)")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetMaxOpenConns(1)
	if _, err := conn.Exec(`BEGIN EXCLUSIVE`); err != nil {
		t.Fatalf("the repaired database cannot be taken exclusively, so the repair left a connection open: %v", err)
	}
	var tables int
	if err := conn.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'signals'`).Scan(&tables); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(`COMMIT`); err != nil {
		t.Fatal(err)
	}
	if tables != 1 {
		t.Fatalf("the repair left %d signals tables, want the rebuilt one", tables)
	}
}

// The signals table a repair rebuilds is the one a new store has: the same
// sqlite_master entries for the table and each of its indexes. db-repair
// rebuilt it from its own copy of the schema's statements, which a change to
// the schema would not have reached.
func TestDBRepair_RebuildsTheSchemasSignalsTable(t *testing.T) {
	fresh := filepath.Join(t.TempDir(), "fresh.db")
	s, err := db.NewStore(fresh)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Init(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	want := signalsSchemaRows(t, fresh)

	path := corruptSignalsDB(t)
	prev := dbPath
	dbPath = path
	t.Cleanup(func() { dbPath = prev })
	cmd := newDBRepairCmd()
	cmd.SetArgs([]string{"--table", "signals", "--no-backup"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	if stderr, err := captureStderr(t, cmd.Execute); err != nil {
		t.Fatalf("db-repair --table signals: %v\n%s", err, stderr)
	}
	if got := signalsSchemaRows(t, path); got != want {
		t.Errorf("the rebuilt signals table's sqlite_master rows:\n%s\nwant a new store's:\n%s", got, want)
	}
}

// signalsSchemaRows renders the sqlite_master rows of the signals table and
// its indexes in the database at path, by name.
func signalsSchemaRows(t *testing.T, path string) string {
	t.Helper()
	conn, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	rows, err := conn.Query(`SELECT type, name, sql FROM sqlite_master WHERE tbl_name = 'signals' ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var b strings.Builder
	for rows.Next() {
		var typ, name, ddl string
		if err := rows.Scan(&typ, &name, &ddl); err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&b, "%s %s: %s\n", typ, name, ddl)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

// The bypass deletes the table's sqlite_master rows and bumps schema_version
// in one transaction, so a failure inside it rolls back to the table as it
// was. failingDB fails the second DELETE, after the first has run.
func TestRunSignalsBypass_AFailureInsideLeavesTheTable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hive.db")
	s, err := db.NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Init(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	schema := func() (tables, indexes int) {
		t.Helper()
		conn, err := sql.Open("sqlite", path)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		if err := conn.QueryRow(`SELECT
			(SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'signals'),
			(SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name LIKE 'idx_signals_%')`).Scan(&tables, &indexes); err != nil {
			t.Fatal(err)
		}
		return tables, indexes
	}
	wantTables, wantIndexes := schema()
	if wantTables != 1 || wantIndexes == 0 {
		t.Fatalf("the store has %d signals tables and %d of their indexes, want the table and its indexes", wantTables, wantIndexes)
	}

	conn := failingDB(t, path, "LIKE 'idx_signals_%'")
	conn.SetMaxOpenConns(1)
	if err := runSignalsBypass(conn); !errors.Is(err, errInjected) {
		t.Fatalf("runSignalsBypass = %v, want the injected failure", err)
	}
	if tables, indexes := schema(); tables != wantTables || indexes != wantIndexes {
		t.Fatalf("after a failed bypass the store has %d signals tables and %d of their indexes, want %d and %d", tables, indexes, wantTables, wantIndexes)
	}
}

// corruptSignalsDB writes a store holding one signal whose row spills onto
// one overflow page, points the row's overflow at page 1, and returns the
// path. A read of the row still completes, on page 1's bytes, and so does
// integrity_check, which reports page 1 referenced twice; DROP TABLE, which
// frees the overflow page, refuses page 1 as corrupt. So the repair reaches
// its writable_schema bypass.
func corruptSignalsDB(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "hive.db")
	root, pageSize := writeOneSpilledSignal(t, path)
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	header := make([]byte, 100)
	page := make([]byte, pageSize)
	if _, err := f.ReadAt(header, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := f.ReadAt(page, (root-1)*pageSize); err != nil {
		t.Fatal(err)
	}
	// Byte 20 of the file header is the space reserved at the end of each
	// page; the rest of the page is usable.
	at := overflowPointerAt(t, page, pageSize-int64(header[20]))
	binary.BigEndian.PutUint32(page[at:], 1)
	if _, err := f.WriteAt(page, (root-1)*pageSize); err != nil {
		t.Fatal(err)
	}
	return path
}

// writeOneSpilledSignal writes a store at path whose signals table holds one
// row too long for its page, and closes it with the WAL checkpointed into
// the file, so no WAL frame shadows a page rewritten afterwards. It returns
// the table's root page and the page size.
func writeOneSpilledSignal(t *testing.T, path string) (root, pageSize int64) {
	t.Helper()
	s, err := db.NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Init(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.WriteDB.Exec(`INSERT INTO signals (signal_type, payload_json) VALUES ('alarm', ?)`, strings.Repeat("x", 5000)); err != nil {
		t.Fatal(err)
	}
	if err := s.ReadDB.QueryRow(`SELECT rootpage FROM sqlite_master WHERE type = 'table' AND name = 'signals'`).Scan(&root); err != nil {
		t.Fatal(err)
	}
	if err := s.ReadDB.QueryRow(`PRAGMA page_size`).Scan(&pageSize); err != nil {
		t.Fatal(err)
	}
	if _, err := s.WriteDB.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(path + "-wal"); err == nil && fi.Size() > 0 {
		t.Fatalf("the WAL still holds %d bytes after the store closed", fi.Size())
	}
	return root, pageSize
}

// overflowPointerAt is the offset of the overflow page number in a table
// leaf page holding one cell. It fails the test unless the cell spills onto
// exactly one overflow page, the case a read of the row survives. A cell is
// its payload size P and rowid as SQLite varints, the payload's local part,
// then the overflow page number. For usable page size U the local part is
// M + (P-M) % (U-4), or M when that exceeds U-35, where M = (U-12)*32/255 -
// 23 (sqlite.org/fileformat2.html, B-tree Pages).
func overflowPointerAt(t *testing.T, page []byte, usable int64) int {
	t.Helper()
	if page[0] != 0x0d || binary.BigEndian.Uint16(page[3:5]) != 1 {
		t.Fatalf("the signals root is not a table leaf holding one cell: type %#x, %d cells", page[0], binary.BigEndian.Uint16(page[3:5]))
	}
	cell := int(binary.BigEndian.Uint16(page[8:10]))
	payload, n := sqliteVarint(page[cell:])
	_, m := sqliteVarint(page[cell+n:])
	minLocal := (usable-12)*32/255 - 23
	local := minLocal + (payload-minLocal)%(usable-4)
	if local > usable-35 {
		local = minLocal
	}
	if payload <= usable-35 || payload-local > usable-4 {
		t.Fatalf("the row's %d bytes do not spill onto exactly one overflow page", payload)
	}
	return cell + n + m + int(local)
}

// sqliteVarint decodes a SQLite varint, big-endian with seven bits a byte
// and the ninth byte whole, and returns it and the bytes it took.
func sqliteVarint(b []byte) (int64, int) {
	var v int64
	for i := 0; i < 8; i++ {
		v = v<<7 | int64(b[i]&0x7f)
		if b[i] < 0x80 {
			return v, i + 1
		}
	}
	return v<<8 | int64(b[8]), 9
}

// captureStderr runs fn and returns what it wrote to os.Stderr.
func captureStderr(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	prev := os.Stderr
	os.Stderr = w
	out := make(chan string)
	go func() {
		b, _ := io.ReadAll(r)
		out <- string(b)
	}()
	runErr := fn()
	os.Stderr = prev
	w.Close()
	return <-out, runErr
}

// errInjected is the failure failingDB gives a statement it refuses.
var errInjected = errors.New("injected failure")

// failingDB opens the database at path through a connector that fails each
// statement holding substr, query or exec, as a disk that refused that one
// read or write would, and runs every other statement on the database. No
// SQL makes such a statement fail on a sound file, so the failure tests
// inject their failure here, at the driver.
func failingDB(t *testing.T, path, substr string) *sql.DB {
	t.Helper()
	probe, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	d := probe.Driver()
	probe.Close()
	conn := sql.OpenDB(failingConnector{d: d, dsn: "file:" + path + "?" + db.BusyTimeoutPragma(), substr: substr})
	t.Cleanup(func() { conn.Close() })
	return conn
}

// failingConnector opens connections to dsn with d that fail the statements
// holding substr.
type failingConnector struct {
	d      driver.Driver
	dsn    string
	substr string
}

func (c failingConnector) Connect(context.Context) (driver.Conn, error) {
	conn, err := c.d.Open(c.dsn)
	if err != nil {
		return nil, err
	}
	return failingConn{Conn: conn, substr: c.substr}, nil
}

func (c failingConnector) Driver() driver.Driver { return c.d }

// failingConn fails a statement that holds substr and runs any other on its
// connection.
type failingConn struct {
	driver.Conn
	substr string
}

func (c failingConn) QueryContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	if strings.Contains(q, c.substr) {
		return nil, errInjected
	}
	return c.Conn.(driver.QueryerContext).QueryContext(ctx, q, args)
}

func (c failingConn) ExecContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	if strings.Contains(q, c.substr) {
		return nil, errInjected
	}
	return c.Conn.(driver.ExecerContext).ExecContext(ctx, q, args)
}
