package store

import (
	"errors"
	"light/internal/config"
	"testing"
)

func TestSettingsPersistenceAndConflict(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { st.Close() }()
	if st.Snapshot().Settings != nil || st.Settings() != config.Default() {
		t.Fatal("legacy defaults")
	}
	value := st.Settings()
	value.HubEnabled = true
	value.HubFetchConcurrency = 8
	if err = st.UpdateSettings(value); err != nil {
		t.Fatal(err)
	}
	if err = st.UpdateSettings(value); !errors.Is(err, ErrConflict) {
		t.Fatal("missing revision conflict", err)
	}
	st.Close()
	st, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	saved := st.Settings()
	if !saved.HubEnabled || saved.HubFetchConcurrency != 8 || saved.Revision != 2 {
		t.Fatal(saved)
	}
	saved.HubCacheMaxBytes = 0
	if err = st.UpdateSettings(saved); err == nil {
		t.Fatal("invalid values persisted")
	}
}
