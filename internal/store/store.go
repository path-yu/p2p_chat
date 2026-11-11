// Package store keeps what the app remembers: settings, and the messages
// of every room, on disk in the user's home.
package store

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

// Msg is one message of a room.
type Msg struct {
	ID   string `json:"id"`
	Room string `json:"room"`
	From string `json:"from"`
	Name string `json:"name"`
	Text string `json:"text"`
	TS   int64  `json:"ts"`
	Mine bool   `json:"mine"`
}

// Settings is what the app remembers between runs.
type Settings struct {
	Name string `json:"name"`
	Room string `json:"room"`
	// Dark is the appearance the user chose, nil until they choose one.
	Dark *bool `json:"dark,omitempty"`
	// DownloadDir is where received files go, empty for the default.
	DownloadDir string `json:"downloadDir,omitempty"`
}

// Store is the app's directory.
type Store struct {
	dir  string
	mu   sync.Mutex
	file *os.File
}

// Open prepares the directory.
func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return &Store{dir: dir}, nil
}

// Dir is the store's directory.
func (s *Store) Dir() string { return s.dir }

// Settings reads the settings, or the defaults.
func (s *Store) Settings() Settings {
	st := Settings{Name: "", Room: "lobby"}
	data, err := os.ReadFile(filepath.Join(s.dir, "settings.json"))
	if err != nil {
		return st
	}
	_ = json.Unmarshal(data, &st)
	if st.Room == "" {
		st.Room = "lobby"
	}
	return st
}

// SaveSettings writes the settings.
func (s *Store) SaveSettings(st Settings) error {
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(s.dir, "settings.json")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Append adds a message to the history.
func (s *Store) Append(m Msg) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.file == nil {
		f, err := os.OpenFile(filepath.Join(s.dir, "history.jsonl"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			return err
		}
		s.file = f
	}
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	_, err = s.file.Write(data)
	return err
}

// History reads the last n messages, oldest first.
func (s *Store) History(n int) []Msg {
	f, err := os.Open(filepath.Join(s.dir, "history.jsonl"))
	if err != nil {
		return nil
	}
	defer f.Close()

	var all []Msg
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		var m Msg
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			continue
		}
		all = append(all, m)
	}
	if n > 0 && len(all) > n {
		all = all[len(all)-n:]
	}
	return all
}
