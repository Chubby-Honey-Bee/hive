package db

import (
	"context"
	"database/sql"
	"fmt"
)

// Tx is one write transaction on a pinned connection of the write pool,
// begun IMMEDIATE so it holds SQLite's write lock from its first statement.
// The write pool has one connection, and while a Tx is open it is the Tx's:
// a write through WriteDB or a repo would wait for it. So every read and
// write that belongs to the transaction goes through the Tx, which also lets
// its reads see its own writes.
type Tx struct {
	ctx  context.Context
	conn *sql.Conn
	done bool
}

// Conn is what a writer shared by the store and a transaction reads and
// writes through: a pool (*sql.DB) or a *Tx. Given the Tx, every statement
// joins the transaction and reads its writes.
type Conn interface {
	Exec(query string, args ...any) (sql.Result, error)
	Query(query string, args ...any) (*sql.Rows, error)
	QueryRow(query string, args ...any) *sql.Row
}

// BeginImmediate starts a Tx. The caller must end it with Commit or
// Rollback.
func (s *Store) BeginImmediate(ctx context.Context) (*Tx, error) {
	conn, err := s.WriteDB.Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("pin connection: %w", err)
	}
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		conn.Close()
		return nil, fmt.Errorf("begin: %w", err)
	}
	return &Tx{ctx: ctx, conn: conn}, nil
}

// Exec runs a statement in the transaction.
func (t *Tx) Exec(query string, args ...any) (sql.Result, error) {
	return t.conn.ExecContext(t.ctx, query, args...)
}

// Query runs a query in the transaction.
func (t *Tx) Query(query string, args ...any) (*sql.Rows, error) {
	return t.conn.QueryContext(t.ctx, query, args...)
}

// QueryContext runs a query in the transaction, bound by ctx as well.
func (t *Tx) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return t.conn.QueryContext(ctx, query, args...)
}

// QueryRow runs a one-row query in the transaction.
func (t *Tx) QueryRow(query string, args ...any) *sql.Row {
	return t.conn.QueryRowContext(t.ctx, query, args...)
}

// Commit commits the transaction and releases the connection. A failed
// commit, a cancelled context's included, rolls back.
func (t *Tx) Commit() error {
	if t.done {
		return fmt.Errorf("commit: transaction already ended")
	}
	t.done = true
	defer t.conn.Close()
	if _, err := t.conn.ExecContext(t.ctx, `COMMIT`); err != nil {
		t.rollback()
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

// Rollback abandons the transaction and releases the connection. After
// Commit it does nothing, so it can be deferred.
func (t *Tx) Rollback() {
	if t.done {
		return
	}
	t.done = true
	t.rollback()
	t.conn.Close()
}

// rollback sends ROLLBACK under a context the transaction's cancellation
// does not reach: the driver does not send a statement under a cancelled
// context, and the write pool would get its connection back inside BEGIN
// IMMEDIATE.
func (t *Tx) rollback() {
	_, _ = t.conn.ExecContext(context.WithoutCancel(t.ctx), `ROLLBACK`)
}
