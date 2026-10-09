package cli

import (
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"
)

// ── db-init ──────────────────────────────────────────────────

func newDBInitCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "db-init",
		Short: "Initialize the HIVE database schema",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := store.Init(); err != nil {
				return err
			}
			abs := dbPath
			if p, err := filepath.Abs(dbPath); err == nil {
				abs = p
			}
			fmt.Printf("HIVE database initialized at %s\n", abs)
			fmt.Println("Schema: CDE-encoded findings with MSS labels")
			fmt.Println("Next: chb db-write dimension '{...}'")
			return nil
		},
	}
}

// ── helpers ──────────────────────────────────────────────────

func intFromMap(m map[string]any, key string) int {
	if v, ok := m[key]; ok {
		switch n := v.(type) {
		case float64:
			return int(n)
		case int:
			return n
		}
	}
	return 0
}

func intFromMapDefault(m map[string]any, key string, def int) int {
	if _, ok := m[key]; !ok {
		return def
	}
	return intFromMap(m, key)
}

func intPtrFromMap(m map[string]any, key string) *int {
	if v, ok := m[key]; ok && v != nil {
		n := intFromMap(m, key)
		return &n
	}
	return nil
}

func int64PtrFromMap(m map[string]any, key string) *int64 {
	if v, ok := m[key]; ok && v != nil {
		switch n := v.(type) {
		case float64:
			i := int64(n)
			return &i
		}
	}
	return nil
}

func stringFromMap(m map[string]any, key string) string {
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

func stringFromMapDefault(m map[string]any, key, def string) string {
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return def
}

func stringPtrFromMap(m map[string]any, key string) *string {
	if v, ok := m[key]; ok && v != nil {
		if s, ok := v.(string); ok {
			return &s
		}
	}
	return nil
}

// ─── Helpers ─────────────────────────────────────────────────────

func anyStr(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprintf("%v", v)
}

func toInt64CLI(v any) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case float64:
		return int64(n)
	case int:
		return int64(n)
	}
	return 0
}
