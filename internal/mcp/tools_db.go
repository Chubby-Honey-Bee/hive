package mcp

// chb_db_write — write CDE-encoded findings/gaps/sources; close gaps/conflicts.

func dbWriteSpec() map[string]any {
	return map[string]any{
		"name":        "chb_db_write",
		"title":       "Write a finding",
		"annotations": map[string]any{"readOnlyHint": false, "destructiveHint": false, "idempotentHint": false, "openWorldHint": false},
		"description": `Write a CDE-encoded record to the workspace database (hive.db). Kinds:
  "finding" { wave, agent, d1-d8, mss_label (assumption|guarantee|definition|unknown), finding, evidence?, source_urls?, depends_on_ids? }
            depends_on_ids is REQUIRED when mss_label is "guarantee": a JSON array of the finding ids it rests on, e.g. [12, 15]
            source_urls is a string or an array of URL strings
  "gap"     { wave, agent, description, priority (critical|important|minor; high→important, medium|low→minor), d1-d4? }
  "source"  { url, title?, wave?, agent?, contribution?, primary_source? }
  "resolve_gap"      { gap_id, wave, agent, finding_id } closes a gap with the finding that answers it
  "resolve_conflict" { conflict_id, wave, resolution } closes a conflict, recording how it was settled`,
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"kind":   map[string]any{"type": "string", "enum": []string{"finding", "gap", "source", "resolve_gap", "resolve_conflict"}, "description": "Which record to write or close"},
				"fields": map[string]any{"type": "object", "description": "The record's fields; the shape per kind is in the tool description"},
			},
			"required": []string{"kind", "fields"},
		},
	}
}

// ─── chb_db_write ────────────────────────────────────────────

// handleDBWrite writes the record through the store's one write path
// (db.Store.WriteRecord), which `chb db-write` and the runner's chb_db_write
// also take.
func (s *mcpServer) handleDBWrite(req rpcRequest, args map[string]any) {
	if s.store == nil {
		s.writeToolResult(req.ID, "", "HIVE_DB_PATH is not set", true)
		return
	}
	kind, _ := args["kind"].(string)
	fields, _ := args["fields"].(map[string]any)
	s.mu.Lock()
	defer s.mu.Unlock()
	_, line, err := s.store.WriteRecord(kind, fields)
	if err != nil {
		s.writeToolResult(req.ID, "", err.Error(), true)
		return
	}
	s.writeToolResult(req.ID, line, "", false)
}
