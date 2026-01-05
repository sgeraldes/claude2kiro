package attachments

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Store manages attachment storage with SHA256-based deduplication
type Store struct {
	baseDir  string
	manifest *Manifest
	mu       sync.RWMutex
}

// Manifest tracks all stored attachments and statistics
type Manifest struct {
	Version     int                        `json:"version"`
	Attachments map[string]*AttachmentMeta `json:"attachments"`
	Stats       ManifestStats              `json:"stats"`
}

// AttachmentMeta contains metadata for a stored attachment
type AttachmentMeta struct {
	Hash       string    `json:"hash"`
	Size       int64     `json:"size"`
	MediaType  string    `json:"media_type"`
	Extension  string    `json:"extension"`
	FirstSeen  time.Time `json:"first_seen"`
	LastSeen   time.Time `json:"last_seen"`
	ReuseCount int       `json:"reuse_count"`
}

// ManifestStats tracks deduplication statistics
type ManifestStats struct {
	TotalFiles        int   `json:"total_files"`
	TotalSizeBytes    int64 `json:"total_size_bytes"`
	SavedByDedupBytes int64 `json:"saved_by_dedup_bytes"`
}

// NewStore creates or loads an attachment store from the given base directory
func NewStore(baseDir string) (*Store, error) {
	s := &Store{
		baseDir: baseDir,
	}

	// Create base directory if it doesn't exist
	if err := os.MkdirAll(baseDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create base directory: %w", err)
	}

	// Load or create manifest
	manifestPath := filepath.Join(baseDir, "manifest.json")
	if _, err := os.Stat(manifestPath); os.IsNotExist(err) {
		// Create new manifest
		s.manifest = &Manifest{
			Version:     1,
			Attachments: make(map[string]*AttachmentMeta),
			Stats:       ManifestStats{},
		}
		if err := s.SaveManifest(); err != nil {
			return nil, fmt.Errorf("failed to save initial manifest: %w", err)
		}
	} else {
		// Load existing manifest
		data, err := os.ReadFile(manifestPath)
		if err != nil {
			return nil, fmt.Errorf("failed to read manifest: %w", err)
		}
		var m Manifest
		if err := json.Unmarshal(data, &m); err != nil {
			return nil, fmt.Errorf("failed to parse manifest: %w", err)
		}
		s.manifest = &m
	}

	return s, nil
}

// Save stores attachment data with deduplication
// Returns the hash, whether it's a new file, and any error
func (s *Store) Save(data []byte, mediaType string) (hash string, isNew bool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Compute SHA256 hash
	h := sha256.Sum256(data)
	hash = hex.EncodeToString(h[:])

	// Check if attachment already exists
	if meta, exists := s.manifest.Attachments[hash]; exists {
		// Update existing attachment metadata
		meta.ReuseCount++
		meta.LastSeen = time.Now()
		s.manifest.Stats.SavedByDedupBytes += int64(len(data))

		if err := s.saveManifestLocked(); err != nil {
			return hash, false, fmt.Errorf("failed to save manifest: %w", err)
		}

		return hash, false, nil
	}

	// New attachment - create sharded directory
	ext := s.getExtension(mediaType)
	shardDir := filepath.Join(s.baseDir, "sha256", hash[:6])
	if err := os.MkdirAll(shardDir, 0755); err != nil {
		return "", false, fmt.Errorf("failed to create shard directory: %w", err)
	}

	// Write file
	filename := hash + ext
	filePath := filepath.Join(shardDir, filename)
	if err := os.WriteFile(filePath, data, 0644); err != nil {
		return "", false, fmt.Errorf("failed to write file: %w", err)
	}

	// Add to manifest
	now := time.Now()
	s.manifest.Attachments[hash] = &AttachmentMeta{
		Hash:       hash,
		Size:       int64(len(data)),
		MediaType:  mediaType,
		Extension:  ext,
		FirstSeen:  now,
		LastSeen:   now,
		ReuseCount: 0,
	}
	s.manifest.Stats.TotalFiles++
	s.manifest.Stats.TotalSizeBytes += int64(len(data))

	// Save manifest
	if err := s.saveManifestLocked(); err != nil {
		return hash, true, fmt.Errorf("failed to save manifest: %w", err)
	}

	return hash, true, nil
}

// Get retrieves attachment data by hash
func (s *Store) Get(hash string) ([]byte, *AttachmentMeta, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	meta, exists := s.manifest.Attachments[hash]
	if !exists {
		return nil, nil, fmt.Errorf("attachment not found: %s", hash)
	}

	// Construct file path
	shardDir := filepath.Join(s.baseDir, "sha256", hash[:6])
	filename := hash + meta.Extension
	filePath := filepath.Join(shardDir, filename)

	// Read file
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to read file: %w", err)
	}

	return data, meta, nil
}

// Exists checks if an attachment with the given hash exists
func (s *Store) Exists(hash string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	_, exists := s.manifest.Attachments[hash]
	return exists
}

// GetAll returns all attachment metadata
func (s *Store) GetAll() []*AttachmentMeta {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make([]*AttachmentMeta, 0, len(s.manifest.Attachments))
	for _, meta := range s.manifest.Attachments {
		result = append(result, meta)
	}
	return result
}

// Delete removes an attachment by hash
func (s *Store) Delete(hash string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	meta, exists := s.manifest.Attachments[hash]
	if !exists {
		return fmt.Errorf("attachment not found: %s", hash)
	}

	// Construct file path
	shardDir := filepath.Join(s.baseDir, "sha256", hash[:6])
	filename := hash + meta.Extension
	filePath := filepath.Join(shardDir, filename)

	// Delete file
	if err := os.Remove(filePath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to delete file: %w", err)
	}

	// Update manifest
	s.manifest.Stats.TotalFiles--
	s.manifest.Stats.TotalSizeBytes -= meta.Size
	s.manifest.Stats.SavedByDedupBytes -= meta.Size * int64(meta.ReuseCount)
	delete(s.manifest.Attachments, hash)

	// Save manifest
	if err := s.saveManifestLocked(); err != nil {
		return fmt.Errorf("failed to save manifest: %w", err)
	}

	return nil
}

// SaveManifest persists the manifest to disk
func (s *Store) SaveManifest() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveManifestLocked()
}

// saveManifestLocked persists the manifest to disk (caller must hold lock)
func (s *Store) saveManifestLocked() error {
	manifestPath := filepath.Join(s.baseDir, "manifest.json")
	tmpPath := manifestPath + ".tmp"

	// Marshal manifest to JSON
	data, err := json.MarshalIndent(s.manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal manifest: %w", err)
	}

	// Write to temporary file
	if err := os.WriteFile(tmpPath, data, 0644); err != nil {
		return fmt.Errorf("failed to write temporary manifest: %w", err)
	}

	// Atomic rename
	if err := os.Rename(tmpPath, manifestPath); err != nil {
		os.Remove(tmpPath) // Clean up temp file on error
		return fmt.Errorf("failed to rename manifest: %w", err)
	}

	return nil
}

// getExtension maps media type to file extension
func (s *Store) getExtension(mediaType string) string {
	switch mediaType {
	case "image/jpeg":
		return ".jpg"
	case "image/png":
		return ".png"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	case "application/pdf":
		return ".pdf"
	case "text/plain":
		return ".txt"
	case "text/html":
		return ".html"
	case "text/csv":
		return ".csv"
	case "application/json":
		return ".json"
	case "application/xml", "text/xml":
		return ".xml"
	default:
		return ""
	}
}
