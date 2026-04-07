package attachments_test

import (
	"fmt"
	"log"
	"os"

	"github.com/sgeraldes/claude2kiro/internal/attachments"
)

func ExampleStore_Save() {
	// Create temporary directory for this example
	tmpDir, err := os.MkdirTemp("", "attachments-example-*")
	if err != nil {
		log.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	// Create store
	store, err := attachments.NewStore(tmpDir)
	if err != nil {
		log.Fatal(err)
	}

	// Save some image data
	imageData := []byte("fake PNG data")
	hash1, isNew1, err := store.Save(imageData, "image/png")
	if err != nil {
		log.Fatal(err)
	}

	fmt.Printf("First save - New: %v, Hash length: %d\n", isNew1, len(hash1))

	// Save the same data again (deduplication)
	hash2, isNew2, err := store.Save(imageData, "image/png")
	if err != nil {
		log.Fatal(err)
	}

	fmt.Printf("Second save - New: %v, Hash length: %d\n", isNew2, len(hash2))
	fmt.Printf("Hashes match: %v\n", hash1 == hash2)

	// Check statistics
	stats := store.GetAll()
	fmt.Printf("Total files: %d\n", len(stats))

	// Output:
	// First save - New: true, Hash length: 64
	// Second save - New: false, Hash length: 64
	// Hashes match: true
	// Total files: 1
}

func ExampleStore_Get() {
	// Create temporary directory for this example
	tmpDir, err := os.MkdirTemp("", "attachments-example-*")
	if err != nil {
		log.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	// Create store
	store, err := attachments.NewStore(tmpDir)
	if err != nil {
		log.Fatal(err)
	}

	// Save data
	originalData := []byte("important document")
	hash, _, err := store.Save(originalData, "application/pdf")
	if err != nil {
		log.Fatal(err)
	}

	// Retrieve data
	retrievedData, meta, err := store.Get(hash)
	if err != nil {
		log.Fatal(err)
	}

	fmt.Printf("Retrieved: %s\n", string(retrievedData))
	fmt.Printf("Media Type: %s\n", meta.MediaType)
	fmt.Printf("Extension: %s\n", meta.Extension)
	fmt.Printf("Size: %d bytes\n", meta.Size)

	// Output:
	// Retrieved: important document
	// Media Type: application/pdf
	// Extension: .pdf
	// Size: 18 bytes
}

func ExampleStore_deduplication() {
	// Create temporary directory for this example
	tmpDir, err := os.MkdirTemp("", "attachments-example-*")
	if err != nil {
		log.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	// Create store
	store, err := attachments.NewStore(tmpDir)
	if err != nil {
		log.Fatal(err)
	}

	// Save the same image 3 times
	imageData := []byte("shared company logo")
	for i := 0; i < 3; i++ {
		_, isNew, err := store.Save(imageData, "image/png")
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("Save %d - New: %v\n", i+1, isNew)
	}

	// Check the metadata
	allFiles := store.GetAll()
	if len(allFiles) == 1 {
		meta := allFiles[0]
		fmt.Printf("\nTotal unique files: 1\n")
		fmt.Printf("Reuse count: %d\n", meta.ReuseCount)
		fmt.Printf("Bytes saved by deduplication: %d\n", len(imageData)*meta.ReuseCount)
	}

	// Output:
	// Save 1 - New: true
	// Save 2 - New: false
	// Save 3 - New: false
	//
	// Total unique files: 1
	// Reuse count: 2
	// Bytes saved by deduplication: 38
}
