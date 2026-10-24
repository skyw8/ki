package output

import (
	"errors"
	"os"
	"strings"
	"testing"
)

func TestProcessOutputQuotaScopesAndRecoveryPrefix(t *testing.T) {
	s := newTestStore(t, Config{MaxProcessFileBytes: 6, MaxProcessSessionBytes: 9, MaxProcessServiceBytes: 12})
	first, err := s.CreateProcessOutputFile("a", "exec_command")
	if err != nil {
		t.Fatal(err)
	}
	n, err := first.Write([]byte("abcdefgh"))
	if n != 6 || !errors.Is(err, ErrProcessBudget) {
		t.Fatalf("file quota: %d %v", n, err)
	}
	raw, err := os.ReadFile(first.Name())
	if err != nil || string(raw) != "abcdef" {
		t.Fatalf("prefix: %q %v", raw, err)
	}
	_ = first.Close()
	second, err := s.CreateProcessOutputFile("a", "exec_command")
	if err != nil {
		t.Fatal(err)
	}
	n, err = second.Write([]byte("123456"))
	if n != 3 || !errors.Is(err, ErrProcessBudget) {
		t.Fatalf("session quota: %d %v", n, err)
	}
	_ = second.Close()
	third, err := s.CreateProcessOutputFile("b", "exec_command")
	if err != nil {
		t.Fatal(err)
	}
	n, err = third.Write([]byte("123456"))
	if n != 3 || !errors.Is(err, ErrProcessBudget) {
		t.Fatalf("service quota: %d %v", n, err)
	}
	if err = s.CloseSession("a"); err != nil {
		t.Fatal(err)
	}
	n, err = third.Write([]byte("456"))
	if n != 3 || err != nil {
		t.Fatalf("released quota: %d %v", n, err)
	}
	_ = third.Close()
	if _, err = s.CreateProcessOutputFile("a", "exec_command"); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("reopened closed session: %v", err)
	}
}

func TestProcessOutputClosedSessionRejectsActiveWriter(t *testing.T) {
	s := newTestStore(t, Config{})
	f, err := s.CreateProcessOutputFile("a", "exec_command")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.Write([]byte("raw")); err != nil {
		t.Fatal(err)
	}
	if err = s.CloseSession("a"); err != nil {
		t.Fatal(err)
	}
	if _, err = f.Write([]byte("more")); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("write after close: %v", err)
	}
	_ = f.Close()
	if s.processTotal != 0 {
		t.Fatalf("closed session charged %d", s.processTotal)
	}
}

func BenchmarkProcessSpoolWrite(b *testing.B) {
	s, err := NewWithConfig(Config{Root: b.TempDir(), MaxProcessFileBytes: 1 << 40, MaxProcessSessionBytes: 1 << 40, MaxProcessServiceBytes: 1 << 40})
	if err != nil {
		b.Fatal(err)
	}
	defer s.Close()
	f, err := s.CreateProcessOutputFile("bench", "exec_command")
	if err != nil {
		b.Fatal(err)
	}
	defer f.Close()
	data := []byte(strings.Repeat("x", 4096))
	b.SetBytes(int64(len(data)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err = f.Write(data); err != nil {
			b.Fatal(err)
		}
	}
}
