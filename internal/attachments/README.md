# Attachments Package

Content-addressable storage for Claude2Kiro with SHA256-based deduplication.

## Features

- **SHA256 Hash-based Deduplication**: Identical files are stored only once
- **Sharded Storage**: Files organized in `sha256/{first6chars}/{fullhash}.{ext}` for filesystem efficiency
- **Manifest Tracking**: JSON-based metadata with reuse statistics
- **Thread-safe**: Concurrent reads and writes using RWMutex
- **Atomic Writes**: Manifest updates use temp file + rename for safety
- **Media Type Support**: Automatic file extension detection for common types

## Directory Structure

```
~/.claude2kiro/attachments/
├── manifest.json                 # Metadata and statistics
└── sha256/                       # Content-addressable storage
    ├── 4601b3/                   # First 6 chars of hash (shard)
    │   └── 4601b302...a8f.png    # Full hash + extension
    └── f11e1b/
        └── f11e1bc5...d2e.jpg
```

## Manifest Format

```json
{
  "version": 1,
  "attachments": {
    "4601b302...a8f": {
      "hash": "4601b302...a8f",
      "size": 12345,
      "media_type": "image/png",
      "extension": ".png",
      "first_seen": "2026-01-03T10:00:00Z",
      "last_seen": "2026-01-03T14:30:00Z",
      "reuse_count": 3
    }
  },
  "stats": {
    "total_files": 42,
    "total_size_bytes": 5242880,
    "saved_by_dedup_bytes": 1048576
  }
}
```

## Usage

```go
import "github.com/sgeraldes/claude2kiro/internal/attachments"

// Create or load store
store, err := attachments.NewStore("~/.claude2kiro/attachments")
if err != nil {
    log.Fatal(err)
}

// Save data (with automatic deduplication)
data := []byte("image data")
hash, isNew, err := store.Save(data, "image/png")
if err != nil {
    log.Fatal(err)
}

if isNew {
    fmt.Println("Saved new file:", hash)
} else {
    fmt.Println("File already exists, deduplicated:", hash)
}

// Retrieve data
data, meta, err := store.Get(hash)
if err != nil {
    log.Fatal(err)
}

fmt.Printf("Size: %d, Media Type: %s, Reused: %d times\n",
    meta.Size, meta.MediaType, meta.ReuseCount)

// Check existence
if store.Exists(hash) {
    fmt.Println("File exists")
}

// Get all attachments
allFiles := store.GetAll()
fmt.Printf("Total files: %d\n", len(allFiles))
```

## Supported Media Types

| Media Type | Extension |
|------------|-----------|
| image/jpeg | .jpg |
| image/png | .png |
| image/gif | .gif |
| image/webp | .webp |
| application/pdf | .pdf |
| text/plain | .txt |
| text/html | .html |
| text/csv | .csv |
| application/json | .json |
| application/xml, text/xml | .xml |
| Other | (no extension) |

## Thread Safety

All public methods are thread-safe using `sync.RWMutex`:
- **Reads** (Get, Exists, GetAll): Multiple concurrent readers allowed
- **Writes** (Save, SaveManifest): Exclusive lock, one writer at a time

## Deduplication Statistics

The manifest tracks:
- **TotalFiles**: Unique files stored
- **TotalSizeBytes**: Total disk space used
- **SavedByDedupBytes**: Bytes saved by deduplication
- **ReuseCount** (per file): How many times a file was deduplicated

## Testing

```bash
# Run all tests
go test ./internal/attachments -v

# Run only examples
go test ./internal/attachments -v -run Example
```

## Implementation Details

### Save Logic

1. Compute SHA256 hash of data
2. Check if hash exists in manifest
3. **If exists**:
   - Increment `ReuseCount`
   - Update `LastSeen` timestamp
   - Update `SavedByDedupBytes` stats
   - Return hash with `isNew=false`
4. **If new**:
   - Create sharded directory
   - Write file to disk
   - Add metadata to manifest
   - Update stats
   - Return hash with `isNew=true`

### Atomic Manifest Updates

1. Marshal manifest to JSON
2. Write to `manifest.json.tmp`
3. Rename to `manifest.json` (atomic on POSIX)
4. Clean up temp file on error

## Log Migration Tool

The migration tool extracts base64 content from bloated log files and replaces them with compact hash references.

### Features

- **Line-by-line Processing**: Handles files with lines up to 300MB using buffered I/O
- **Smart Detection**: Uses regex to find base64 content in JSON fields (`data`, `bytes`, `content`, etc.)
- **Media Type Awareness**: Detects nearby media_type fields for accurate file extension mapping
- **Deduplication Tracking**: Reports unique vs duplicate attachments
- **Atomic Replacement**: Uses temp file + rename to prevent data loss
- **No Backups**: Directly replaces original file (as per requirements)

### Usage

```go
import "github.com/sgeraldes/claude2kiro/internal/attachments"

// Create store
store, err := attachments.NewStore("~/.claude2kiro/attachments")
if err != nil {
    log.Fatal(err)
}

// Migrate a single log file
result, err := attachments.MigrateLogFile("~/.claude2kiro/logs/2026-01-03.log", store)
if err != nil {
    log.Fatal(err)
}

fmt.Println(result.FormatSummary())
// Output:
// File: 2026-01-03.log
//   Original size:      150.3MB
//   New size:           2.1MB
//   Space saved:        148.2MB (98.6%)
//   Attachments found:  45
//   Unique:             12
//   Duplicates:         33

// Migrate all log files in a directory
results, err := attachments.MigrateAllLogs("~/.claude2kiro/logs", store)
if err != nil {
    log.Fatal(err)
}

for _, result := range results {
    fmt.Println(result.FormatSummary())
}
```

### Attachment Reference Format

Base64 content is replaced with:
```
@attachment:sha256:{hash}:{humanSize}:{mediaType}
```

Example:
```json
{"data": "@attachment:sha256:4601b302...a8f:12.5KB:image/png"}
```

### Migration Algorithm

1. Open log file for reading with 1MB buffer
2. Create temporary output file
3. For each line:
   - Quick check: does line contain base64 field names?
   - Extract nearby media types for context
   - Find all base64 blobs using regex (minimum 100 chars)
   - For each blob:
     - Validate by decoding sample (first 100 chars)
     - Decode full base64 content
     - Determine media type from nearby JSON fields
     - Save to store (automatic deduplication)
     - Replace with `@attachment:sha256:...` reference
4. Write cleaned line to temp file
5. After all lines: atomic rename temp → original
6. Calculate and return statistics

### Statistics Tracked

- **OriginalSize**: File size before migration
- **NewSize**: File size after migration
- **SpaceSaved**: Bytes saved (OriginalSize - NewSize)
- **AttachmentsFound**: Total base64 blobs detected
- **UniqueAttachments**: Unique files stored
- **DuplicatesRemoved**: Duplicate base64 blobs deduplicated

## Future Enhancements

- Garbage collection for unreferenced files
- LRU cache for frequently accessed files
- Compression for text-based attachments
- Background manifest persistence (async writes)
- Checksum verification on read
- Reverse migration tool (restore base64 from attachments)
