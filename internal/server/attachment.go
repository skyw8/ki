package server

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

const maxAttachmentBytes = 25 << 20

func (s *Server) uploadAttachment(w http.ResponseWriter, r *http.Request) {
	if s.running(r.PathValue("id")) {
		http.Error(w, "session busy", http.StatusConflict)
		return
	}
	sess, err := s.open(r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	defer func() { _ = sess.Close() }()

	r.Body = http.MaxBytesReader(w, r.Body, maxAttachmentBytes+1)
	if err := r.ParseMultipartForm(maxAttachmentBytes); err != nil {
		http.Error(w, "attachment exceeds 25 MiB or is invalid", http.StatusBadRequest)
		return
	}
	f, hdr, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "file required", http.StatusBadRequest)
		return
	}
	defer func() { _ = f.Close() }()
	b, err := io.ReadAll(io.LimitReader(f, maxAttachmentBytes+1))
	if err != nil || len(b) == 0 || len(b) > maxAttachmentBytes {
		http.Error(w, "attachment exceeds 25 MiB or is empty", http.StatusBadRequest)
		return
	}
	sum := sha256.Sum256(b)
	id := hex.EncodeToString(sum[:])
	ext := strings.ToLower(filepath.Ext(filepath.Base(hdr.Filename)))
	if len(ext) > 16 {
		ext = ""
	}
	dir := filepath.Join(sess.Dir, "attachments")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	path := filepath.Join(dir, id+ext)
	// Content-addressed names make retries idempotent. O_EXCL prevents a
	// concurrent upload from truncating a blob already referenced by a branch.
	out, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600) //nolint:gosec // path is a content-addressed file under the session directory
	if err == nil {
		if _, err = out.Write(b); err == nil {
			err = out.Sync()
		}
		_ = out.Close()
	}
	if err != nil && !os.IsExist(err) {
		http.Error(w, fmt.Sprintf("store attachment: %v", err), http.StatusInternalServerError)
		return
	}
	mimeType := hdr.Header.Get("Content-Type")
	if mimeType == "" || mimeType == "application/octet-stream" {
		mimeType = http.DetectContentType(b)
	}
	typ := "file"
	if strings.HasPrefix(mimeType, "image/") {
		typ = "image"
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"type": typ, "id": id, "path": path, "name": filepath.Base(hdr.Filename),
		"mimeType": mimeType, "size": len(b),
	})
}
