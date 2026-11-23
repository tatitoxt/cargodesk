package httpapi

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"

	"github.com/tatitoxt/cargodesk/api/internal/store"
)

// safeName strips path separators and shell metacharacters from an uploaded
// filename before it ever touches the filesystem.
var safeName = regexp.MustCompile(`[^A-Za-z0-9._-]`)

func sanitizeFilename(name string) string {
	name = filepath.Base(name)
	name = safeName.ReplaceAllString(name, "_")
	if name == "" || name == "." || name == ".." {
		name = "file"
	}
	if len(name) > 120 {
		name = name[:120]
	}
	return name
}

func newStorageKey(orgID, filename string) string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return filepath.Join(orgID, hex.EncodeToString(b[:])+"-"+sanitizeFilename(filename))
}

// blobPath resolves a stored key inside AttachmentsDir and refuses escapes.
func (s *Server) blobPath(key string) (string, error) {
	root, err := filepath.Abs(s.cfg.AttachmentsDir)
	if err != nil {
		return "", err
	}
	p := filepath.Join(root, filepath.FromSlash(key))
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	if abs != p || len(abs) <= len(root) || abs[:len(root)] != root {
		return "", fmt.Errorf("storage key escapes attachments dir")
	}
	return abs, nil
}

// POST /api/v1/shipments/{id}/attachments — multipart "file" field (editor+).
func (s *Server) handleUploadAttachment(w http.ResponseWriter, r *http.Request) {
	if s.cfg.AttachmentsDir == "" {
		writeError(w, r, http.StatusServiceUnavailable, "attachments_disabled",
			"attachment storage is not configured on this server", nil)
		return
	}
	maxBytes := s.cfg.MaxAttachmentBytes
	if maxBytes <= 0 {
		maxBytes = 10 << 20 // 10 MiB
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes+1<<20)
	if err := r.ParseMultipartForm(maxBytes); err != nil {
		writeError(w, r, http.StatusRequestEntityTooLarge, "payload_too_large",
			fmt.Sprintf("attachment exceeds %d bytes", maxBytes), nil)
		return
	}
	file, hdr, err := r.FormFile("file")
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "missing_file", "send a multipart field named 'file'", nil)
		return
	}
	defer file.Close()

	a, shipmentID := actor(r), r.PathValue("id")
	key := newStorageKey(a.Org.ID, hdr.Filename)
	path, err := s.blobPath(key)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "storage_error", err.Error(), nil)
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		s.writeDomainError(w, r, err)
		return
	}
	dst, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o640)
	if err != nil {
		s.writeDomainError(w, r, err)
		return
	}
	h := sha256.New()
	size, copyErr := io.Copy(io.MultiWriter(dst, h), io.LimitReader(file, maxBytes+1))
	closeErr := dst.Close()
	if copyErr != nil || closeErr != nil || size > maxBytes {
		_ = os.Remove(path)
		if size > maxBytes {
			writeError(w, r, http.StatusRequestEntityTooLarge, "payload_too_large",
				fmt.Sprintf("attachment exceeds %d bytes", maxBytes), nil)
			return
		}
		s.writeDomainError(w, r, fmt.Errorf("%v %v", copyErr, closeErr))
		return
	}

	contentType := hdr.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	att, err := s.cfg.Store.SaveAttachment(r.Context(), a, shipmentID, store.Attachment{
		Filename: sanitizeFilename(hdr.Filename), ContentType: contentType,
		ByteSize: size, SHA256: hex.EncodeToString(h.Sum(nil)), StorageKey: key,
	}, s.now(), correlationID(r))
	if err != nil {
		_ = os.Remove(path)
		s.writeDomainError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(att)
}

// GET /api/v1/shipments/{id}/attachments — metadata list (any member).
func (s *Server) handleListAttachments(w http.ResponseWriter, r *http.Request) {
	atts, err := s.cfg.Store.ListAttachments(r.Context(), actor(r), r.PathValue("id"))
	if err != nil {
		s.writeDomainError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"attachments": atts})
}

// GET /api/v1/shipments/{id}/attachments/{aid} — download bytes (any member).
func (s *Server) handleDownloadAttachment(w http.ResponseWriter, r *http.Request) {
	att, err := s.cfg.Store.GetAttachment(r.Context(), actor(r), r.PathValue("id"), r.PathValue("aid"))
	if err != nil {
		s.writeDomainError(w, r, err)
		return
	}
	path, err := s.blobPath(att.StorageKey)
	if err != nil {
		s.writeDomainError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", att.ContentType)
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", att.Filename))
	http.ServeFile(w, r, path)
}

// DELETE /api/v1/shipments/{id}/attachments/{aid} — removes metadata + blob (editor+).
func (s *Server) handleDeleteAttachment(w http.ResponseWriter, r *http.Request) {
	att, err := s.cfg.Store.DeleteAttachment(r.Context(), actor(r), r.PathValue("id"), r.PathValue("aid"), s.now(), correlationID(r))
	if err != nil {
		s.writeDomainError(w, r, err)
		return
	}
	if path, perr := s.blobPath(att.StorageKey); perr == nil {
		_ = os.Remove(path) // best-effort; metadata row is already gone
	}
	w.WriteHeader(http.StatusNoContent)
}
