package cli

import (
	"regexp"
	"testing"
)

// A flag named in a usage line must exist on the command.
func TestGenerateUsageNamesOnlyDefinedFlags(t *testing.T) {
	cmd := newSwarmGenerateCmd()
	for _, m := range regexp.MustCompile(`--([a-z][a-z0-9-]*)`).FindAllStringSubmatch(cmd.Use, -1) {
		if cmd.Flags().Lookup(m[1]) == nil {
			t.Errorf("Use %q names --%s, which generate does not define", cmd.Use, m[1])
		}
	}
}
