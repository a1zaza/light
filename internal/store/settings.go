package store

import "light/internal/config"

func (s *Store) Settings() config.Settings {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.state.Settings == nil {
		return config.Default()
	}
	return *s.state.Settings
}

func (s *Store) UpdateSettings(value config.Settings) error {
	value, err := value.Normalize()
	if err != nil {
		return err
	}
	return s.Change(func(st *State) error {
		current := config.Default()
		if st.Settings != nil {
			current = *st.Settings
		}
		if current.Revision != value.Revision {
			return ErrConflict
		}
		value.Revision++
		st.Settings = &value
		return nil
	})
}
