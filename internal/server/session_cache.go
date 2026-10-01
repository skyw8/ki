package server

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"ki/internal/session"
)

// sessionSnap is one read of a session for one request.
//
// entries is a window of the transcript, not necessarily all of it: a request
// that only needs the conversation tail reads the tail (complete is false), and
// the read is served from the process-wide entry cache in internal/session, so
// repeated opens of a long session stay proportional to what was appended
// rather than to the size of the history.
type sessionSnap struct {
	id         string
	dir        string
	header     session.Header
	configV    session.Config
	leafID     string
	title      string
	entries    []session.Entry
	complete   bool
	transcript *session.Transcript
	// small marks a transcript that one tail read covers entirely, so its index
	// costs no extra read and can be answered inline.
	small bool
}

// loadSessionSnap reads full bodies for model execution/diagnostics, or a tail
// window for ordinary detailed opens. want == 0 skips transcript decoding.
// Historical views use loadIndexedSessionSnap and hydrate selected offsets.
func (s *Server) loadSessionSnap(id string, full bool, want int) (*sessionSnap, error) {
	return s.readSessionSnap(id, full, want, false)
}

func (s *Server) loadIndexedSessionSnap(id string) (*sessionSnap, error) {
	return s.readSessionSnap(id, true, 0, true)
}

func (s *Server) readSessionSnap(id string, full bool, want int, indexed bool) (*sessionSnap, error) {
	dir, err := s.sessionDir(id)
	if err != nil {
		return nil, err
	}
	header, err := session.ReadHeader(dir)
	if err != nil {
		return nil, fmt.Errorf("read session header: %w", err)
	}
	cfg, err := session.ReadConfig(dir)
	if err != nil {
		return nil, fmt.Errorf("read session config: %w", err)
	}
	snap := &sessionSnap{
		id:       header.ID,
		dir:      dir,
		header:   header,
		configV:  cfg,
		leafID:   cfg.ActiveLeafID,
		title:    cfg.Title,
		complete: true,
	}
	if indexed {
		transcript, err := session.ReadTranscript(dir)
		if err != nil {
			return nil, err
		}
		snap.transcript, snap.entries = transcript, transcript.Entries()
	} else if full {
		entries, err := session.AllEntries(dir)
		if err != nil {
			return nil, err
		}
		snap.entries = entries
	} else if want > 0 {
		entries, complete, err := session.LeafTail(dir, cfg.ActiveLeafID, want)
		if err != nil {
			return nil, err
		}
		snap.entries, snap.complete = entries, complete
		if info, err := os.Stat(filepath.Join(dir, "events.jsonl")); err == nil {
			snap.small = info.Size() <= session.TailReadLimit
		}
	}
	if (full || want > 0) && !entryIn(snap.entries, snap.leafID) {
		// The active leaf is not in the window: either config points at a
		// deleted entry or (impossible for a tail read, see LeafTail) the
		// window missed it. Mirror session.Open and use the newest non-sideband
		// entry that was read.
		snap.leafID = lastNonSideband(snap.entries)
	}
	if snap.title == "" && (!full || indexed) {
		// Why: the tail window does not contain the session's first user
		// message, so TitleFrom would fall back to the newest one and rename
		// the session. The lite row is cached and only revalidates two stats.
		if info, err := s.slist.Row(dir); err == nil {
			snap.title = info.Title
		}
	}
	if snap.title == "" {
		snap.title = session.TitleFrom(cfg, snap.entries)
	}
	return snap, nil
}

func entryIn(entries []session.Entry, id string) bool {
	if id == "" {
		return false
	}
	return slices.ContainsFunc(entries, func(e session.Entry) bool { return e.ID == id })
}

func lastNonSideband(entries []session.Entry) string {
	for i := len(entries) - 1; i >= 0; i-- {
		if !entries[i].Sideband {
			return entries[i].ID
		}
	}
	return ""
}

func (s *Server) dropSessionSnap(id string) {
	if dir, ok := s.sidx.Lookup(id); ok {
		session.DropEntriesCache(dir)
	}
}

func (s *Server) sessionDir(id string) (string, error) {
	if dir, ok := s.sidx.Lookup(id); ok {
		if _, err := os.Stat(filepath.Join(dir, "events.jsonl")); err == nil {
			return dir, nil
		}
		s.sidx.Remove(id)
	}
	dir, err := session.Find(s.cfg.Sessions.Root, id)
	if err != nil {
		return "", fmt.Errorf("find session: %w", err)
	}
	s.sidx.Add(id, dir)
	return dir, nil
}

func (snap *sessionSnap) index() []session.IndexEntry {
	if snap.transcript != nil {
		return snap.transcript.Index()
	}
	return session.BuildIndex(snap.entries)
}

// systemPrompt returns the system prompt of the newest request_header on the
// active leaf. The body-free transcript snapshot clears that field, so the
// header row is re-read by offset; the fallback covers a full snapshot whose
// bodies were never dropped.
func (snap *sessionSnap) systemPrompt() string {
	entries := snap.entries
	if snap.transcript != nil {
		entries = snap.transcript.Entries()
	}
	id := ""
	for _, e := range slices.Backward(session.LeafChain(entries, snap.leafID)) {
		if e.Type == "request_header" {
			id = e.ID
			break
		}
	}
	if id == "" {
		return ""
	}
	if snap.transcript != nil {
		if got, err := snap.transcript.Lookup([]string{id}); err == nil && len(got) > 0 {
			return got[0].System
		}
	}
	for _, e := range entries {
		if e.ID == id {
			return e.System
		}
	}
	return ""
}
