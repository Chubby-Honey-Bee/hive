// Package db is the SQLite store behind every chb command: one database
// per workspace (hive.db), opened by NewStore with a single-connection
// write pool and a parallel read pool. Store owns the pools and the schema
// (Init), and reaches one repository per table family through typed
// accessors (Findings, Gaps, Workflows, Comb, …). Cross-table operations —
// the MSS audit (RunAudit), the cascade primitives and the summary — live
// on Store, and so does WriteRecord, the one write path of the records
// chb_db_write and chb db-write share. BeginImmediate opens a write transaction (Tx), which the
// writers shared by the store and a transaction accept as a Conn.
package db
