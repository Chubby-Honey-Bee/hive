package db

import (
	"database/sql"
	"encoding/json"
	"fmt"
)

// SignalsRepo owns the signals table.
type SignalsRepo struct {
	writeDB *sql.DB
	readDB  *sql.DB
}

// newSignalsRepo binds the two pools.
func newSignalsRepo(writeDB, readDB *sql.DB) *SignalsRepo {
	return &SignalsRepo{writeDB: writeDB, readDB: readDB}
}

// EmitSignal writes a signal to the signals table.
func (r *SignalsRepo) EmitSignal(signalType string, sourceType *string, sourceID *int64,
	targetD1, targetD2, targetD3, targetD4 *int, payload any, wave *int,
) (int64, error) {
	return EmitSignalOn(r.writeDB, signalType, sourceType, sourceID, targetD1, targetD2, targetD3, targetD4, payload, wave)
}

// EmitSignalOn is EmitSignal through c, a pool or a Tx.
func EmitSignalOn(c Conn, signalType string, sourceType *string, sourceID *int64,
	targetD1, targetD2, targetD3, targetD4 *int, payload any, wave *int,
) (int64, error) {
	payloadJSON := "{}"
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return 0, fmt.Errorf("marshal signal payload: %w", err)
		}
		payloadJSON = string(b)
	}

	res, err := c.Exec(
		`INSERT INTO signals
		 (signal_type, source_type, source_id, target_d1, target_d2, target_d3, target_d4, payload_json, wave)
		 VALUES (?,?,?,?,?,?,?,?,?)`,
		signalType, sourceType, sourceID, targetD1, targetD2, targetD3, targetD4, payloadJSON, wave,
	)
	if err != nil {
		return 0, fmt.Errorf("emit signal: %w", err)
	}
	return res.LastInsertId()
}
