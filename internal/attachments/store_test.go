package attachments

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNewStore(t *testing.T) {
	// Create temporary directory
	tmpDir, err := os.MkdirTemp("", "store-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create store
	store, err := NewStore(tmpDir)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}

	// Verify manifest was created
	if store.manifest == nil {
		t.Fatal("Manifest is nil")
	}
	if store.manifest.Version != 1 {
		t.Errorf("Expected version 1, got %d", store.manifest.Version)
	}
	if store.manifest.Attachments == nil {
		t.Fatal("Attachments map is nil")
	}

	// Verify manifest file was created
	manifestPath := filepath.Join(tmpDir, "manifest.json")
	if _, err := os.Stat(manifestPath); os.IsNotExist(err) {
		t.Fatal("Manifest file was not created")
	}
}

func TestStoreLoadExisting(t *testing.T) {
	// Create temporary directory
	tmpDir, err := os.MkdirTemp("", "store-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create initial store and save data
	store1, err := NewStore(tmpDir)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}

	data := []byte("test data")
	hash1, _, err := store1.Save(data, "text/plain")
	if err != nil {
		t.Fatalf("Failed to save data: %v", err)
	}

	// Create new store instance pointing to same directory
	store2, err := NewStore(tmpDir)
	if err != nil {
		t.Fatalf("Failed to load existing store: %v", err)
	}

	// Verify loaded data
	if !store2.Exists(hash1) {
		t.Error("Hash not found in loaded store")
	}

	retrieved, meta, err := store2.Get(hash1)
	if err != nil {
		t.Fatalf("Failed to get data: %v", err)
	}
	if string(retrieved) != string(data) {
		t.Errorf("Retrieved data mismatch: got %q, want %q", retrieved, data)
	}
	if meta.MediaType != "text/plain" {
		t.Errorf("Media type mismatch: got %q, want %q", meta.MediaType, "text/plain")
	}
}

func TestSaveNewFile(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "store-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := NewStore(tmpDir)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}

	data := []byte("hello world")
	expectedHash := computeHash(data)

	hash, isNew, err := store.Save(data, "text/plain")
	if err != nil {
		t.Fatalf("Failed to save: %v", err)
	}

	if hash != expectedHash {
		t.Errorf("Hash mismatch: got %q, want %q", hash, expectedHash)
	}
	if !isNew {
		t.Error("Expected isNew=true for new file")
	}

	// Verify file was created in correct location
	shardDir := filepath.Join(tmpDir, "sha256", hash[:6])
	filePath := filepath.Join(shardDir, hash+".txt")
	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		t.Errorf("File not created at expected path: %s", filePath)
	}

	// Verify manifest was updated
	meta, exists := store.manifest.Attachments[hash]
	if !exists {
		t.Fatal("Attachment not in manifest")
	}
	if meta.Hash != hash {
		t.Errorf("Hash mismatch in metadata: got %q, want %q", meta.Hash, hash)
	}
	if meta.Size != int64(len(data)) {
		t.Errorf("Size mismatch: got %d, want %d", meta.Size, len(data))
	}
	if meta.MediaType != "text/plain" {
		t.Errorf("MediaType mismatch: got %q, want %q", meta.MediaType, "text/plain")
	}
	if meta.Extension != ".txt" {
		t.Errorf("Extension mismatch: got %q, want %q", meta.Extension, ".txt")
	}
	if meta.ReuseCount != 0 {
		t.Errorf("ReuseCount should be 0 for new file, got %d", meta.ReuseCount)
	}

	// Verify stats
	if store.manifest.Stats.TotalFiles != 1 {
		t.Errorf("TotalFiles should be 1, got %d", store.manifest.Stats.TotalFiles)
	}
	if store.manifest.Stats.TotalSizeBytes != int64(len(data)) {
		t.Errorf("TotalSizeBytes mismatch: got %d, want %d", store.manifest.Stats.TotalSizeBytes, len(data))
	}
}

func TestSaveDuplicate(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "store-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := NewStore(tmpDir)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}

	data := []byte("duplicate test")

	// Save first time
	hash1, isNew1, err := store.Save(data, "text/plain")
	if err != nil {
		t.Fatalf("Failed to save first time: %v", err)
	}
	if !isNew1 {
		t.Error("First save should be new")
	}

	// Save second time (duplicate)
	time.Sleep(10 * time.Millisecond) // Ensure different timestamps
	hash2, isNew2, err := store.Save(data, "text/plain")
	if err != nil {
		t.Fatalf("Failed to save second time: %v", err)
	}

	if hash2 != hash1 {
		t.Errorf("Hash mismatch: got %q, want %q", hash2, hash1)
	}
	if isNew2 {
		t.Error("Second save should not be new")
	}

	// Verify metadata updated
	meta := store.manifest.Attachments[hash1]
	if meta.ReuseCount != 1 {
		t.Errorf("ReuseCount should be 1, got %d", meta.ReuseCount)
	}
	if meta.LastSeen.Equal(meta.FirstSeen) {
		t.Error("LastSeen should be updated")
	}

	// Verify stats
	if store.manifest.Stats.TotalFiles != 1 {
		t.Errorf("TotalFiles should still be 1, got %d", store.manifest.Stats.TotalFiles)
	}
	if store.manifest.Stats.SavedByDedupBytes != int64(len(data)) {
		t.Errorf("SavedByDedupBytes mismatch: got %d, want %d", store.manifest.Stats.SavedByDedupBytes, len(data))
	}

	// Save third time
	hash3, isNew3, err := store.Save(data, "text/plain")
	if err != nil {
		t.Fatalf("Failed to save third time: %v", err)
	}
	if hash3 != hash1 || isNew3 {
		t.Error("Third save should also be duplicate")
	}
	if meta.ReuseCount != 2 {
		t.Errorf("ReuseCount should be 2, got %d", meta.ReuseCount)
	}
	if store.manifest.Stats.SavedByDedupBytes != int64(len(data))*2 {
		t.Errorf("SavedByDedupBytes should be %d, got %d", len(data)*2, store.manifest.Stats.SavedByDedupBytes)
	}
}

func TestGet(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "store-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := NewStore(tmpDir)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}

	data := []byte("test data for retrieval")
	hash, _, err := store.Save(data, "text/plain")
	if err != nil {
		t.Fatalf("Failed to save: %v", err)
	}

	// Retrieve
	retrieved, meta, err := store.Get(hash)
	if err != nil {
		t.Fatalf("Failed to get: %v", err)
	}

	if string(retrieved) != string(data) {
		t.Errorf("Data mismatch: got %q, want %q", retrieved, data)
	}
	if meta.Hash != hash {
		t.Errorf("Hash mismatch: got %q, want %q", meta.Hash, hash)
	}
	if meta.MediaType != "text/plain" {
		t.Errorf("MediaType mismatch: got %q, want %q", meta.MediaType, "text/plain")
	}
}

func TestGetNotFound(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "store-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := NewStore(tmpDir)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}

	_, _, err = store.Get("nonexistent-hash")
	if err == nil {
		t.Error("Expected error for nonexistent hash")
	}
}

func TestExists(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "store-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := NewStore(tmpDir)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}

	data := []byte("test existence")
	hash, _, err := store.Save(data, "text/plain")
	if err != nil {
		t.Fatalf("Failed to save: %v", err)
	}

	if !store.Exists(hash) {
		t.Error("Hash should exist")
	}
	if store.Exists("nonexistent") {
		t.Error("Nonexistent hash should not exist")
	}
}

func TestGetAll(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "store-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := NewStore(tmpDir)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}

	// Save multiple files
	files := []struct {
		data      []byte
		mediaType string
	}{
		{[]byte("file 1"), "text/plain"},
		{[]byte("file 2"), "image/png"},
		{[]byte("file 3"), "application/pdf"},
	}

	for _, f := range files {
		if _, _, err := store.Save(f.data, f.mediaType); err != nil {
			t.Fatalf("Failed to save: %v", err)
		}
	}

	// Get all
	all := store.GetAll()
	if len(all) != len(files) {
		t.Errorf("Expected %d attachments, got %d", len(files), len(all))
	}
}

func TestDelete(t *testing.T) {
	// Create temporary directory
	tmpDir, err := os.MkdirTemp("", "store-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create store
	store, err := NewStore(tmpDir)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}

	// Save a file
	data := []byte("test content")
	hash, _, err := store.Save(data, "text/plain")
	if err != nil {
		t.Fatalf("Failed to save file: %v", err)
	}

	// Verify file exists
	if !store.Exists(hash) {
		t.Fatal("File should exist after save")
	}

	// Check stats before delete
	if store.manifest.Stats.TotalFiles != 1 {
		t.Errorf("Expected 1 total file, got %d", store.manifest.Stats.TotalFiles)
	}
	if store.manifest.Stats.TotalSizeBytes != int64(len(data)) {
		t.Errorf("Expected %d total bytes, got %d", len(data), store.manifest.Stats.TotalSizeBytes)
	}

	// Delete the file
	err = store.Delete(hash)
	if err != nil {
		t.Fatalf("Failed to delete file: %v", err)
	}

	// Verify file no longer exists
	if store.Exists(hash) {
		t.Fatal("File should not exist after delete")
	}

	// Verify stats were updated
	if store.manifest.Stats.TotalFiles != 0 {
		t.Errorf("Expected 0 total files after delete, got %d", store.manifest.Stats.TotalFiles)
	}
	if store.manifest.Stats.TotalSizeBytes != 0 {
		t.Errorf("Expected 0 total bytes after delete, got %d", store.manifest.Stats.TotalSizeBytes)
	}

	// Verify Get returns error for deleted file
	_, _, err = store.Get(hash)
	if err == nil {
		t.Fatal("Expected error when getting deleted file")
	}

	// Verify deleting non-existent file returns error
	err = store.Delete("nonexistent")
	if err == nil {
		t.Fatal("Expected error when deleting non-existent file")
	}
}

func TestGetExtension(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "store-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := NewStore(tmpDir)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}

	tests := []struct {
		mediaType string
		want      string
	}{
		{"image/jpeg", ".jpg"},
		{"image/png", ".png"},
		{"image/gif", ".gif"},
		{"image/webp", ".webp"},
		{"application/pdf", ".pdf"},
		{"text/plain", ".txt"},
		{"text/html", ".html"},
		{"text/csv", ".csv"},
		{"application/json", ".json"},
		{"application/xml", ".xml"},
		{"text/xml", ".xml"},
		{"application/octet-stream", ""},
		{"unknown/type", ""},
	}

	for _, tt := range tests {
		got := store.getExtension(tt.mediaType)
		if got != tt.want {
			t.Errorf("getExtension(%q) = %q, want %q", tt.mediaType, got, tt.want)
		}
	}
}

func TestMultipleMediaTypes(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "store-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := NewStore(tmpDir)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}

	tests := []struct {
		data      []byte
		mediaType string
		ext       string
	}{
		{[]byte("text content"), "text/plain", ".txt"},
		{[]byte("png binary"), "image/png", ".png"},
		{[]byte("pdf binary"), "application/pdf", ".pdf"},
		{[]byte("json data"), "application/json", ".json"},
	}

	for _, tt := range tests {
		hash, _, err := store.Save(tt.data, tt.mediaType)
		if err != nil {
			t.Fatalf("Failed to save %s: %v", tt.mediaType, err)
		}

		meta := store.manifest.Attachments[hash]
		if meta.Extension != tt.ext {
			t.Errorf("Extension mismatch for %s: got %q, want %q", tt.mediaType, meta.Extension, tt.ext)
		}

		// Verify file exists with correct extension
		shardDir := filepath.Join(tmpDir, "sha256", hash[:6])
		filePath := filepath.Join(shardDir, hash+tt.ext)
		if _, err := os.Stat(filePath); os.IsNotExist(err) {
			t.Errorf("File not created at expected path: %s", filePath)
		}
	}
}

func TestConcurrentAccess(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "store-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := NewStore(tmpDir)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}

	// Save initial data
	data := []byte("concurrent test")
	hash, _, err := store.Save(data, "text/plain")
	if err != nil {
		t.Fatalf("Failed to save: %v", err)
	}

	// Concurrent reads and writes
	done := make(chan bool)
	for i := 0; i < 10; i++ {
		go func(id int) {
			// Read
			_, _, err := store.Get(hash)
			if err != nil {
				t.Errorf("Goroutine %d: failed to get: %v", id, err)
			}

			// Write duplicate
			_, _, err = store.Save(data, "text/plain")
			if err != nil {
				t.Errorf("Goroutine %d: failed to save: %v", id, err)
			}

			// Check existence
			if !store.Exists(hash) {
				t.Errorf("Goroutine %d: hash should exist", id)
			}

			done <- true
		}(i)
	}

	// Wait for all goroutines
	for i := 0; i < 10; i++ {
		<-done
	}
}

// Helper function to compute SHA256 hash
func computeHash(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}
