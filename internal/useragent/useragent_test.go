package useragent

import "testing"

func TestFor(t *testing.T) {
	prev := Version
	Version = "1.2.3"
	t.Cleanup(func() { Version = prev })
	want := "chb/1.2.3 (+https://github.com/Chubby-Honey-Bee/hive) validate-sources"
	if got := For("validate-sources"); got != want {
		t.Errorf("For = %q, want %q", got, want)
	}
}
