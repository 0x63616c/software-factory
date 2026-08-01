// Package blobs defines durable names for stored blobs.
package blobs

import (
	"fmt"
	"strings"
)

// Bucket identifies what a blob is for. Buckets are a closed set because their
// names define the top-level on-disk layout.
type Bucket string

const (
	// BucketPayloads contains payload blobs.
	BucketPayloads Bucket = "payloads"
	// BucketConversations contains immutable agent conversation revisions.
	BucketConversations Bucket = "conversations"
)

// Key names one blob.
type Key struct {
	Bucket Bucket
	Path   string // Path is slash-separated with no leading or trailing slash.
}

// NewKey creates a valid blob key.
func NewKey(bucket Bucket, path string) (Key, error) {
	if err := validate(bucket, path); err != nil {
		return Key{}, err
	}

	return Key{Bucket: bucket, Path: path}, nil
}

// ParseKey parses a blob key in its string form.
func ParseKey(value string) (Key, error) {
	bucket, path, found := strings.Cut(value, "/")
	if !found {
		return Key{}, fmt.Errorf("parse blob key %q: missing bucket/path separator", value)
	}

	key, err := NewKey(Bucket(bucket), path)
	if err != nil {
		return Key{}, fmt.Errorf("parse blob key %q: %w", value, err)
	}

	return key, nil
}

// String returns the string form of a blob key.
func (key Key) String() string {
	return string(key.Bucket) + "/" + key.Path
}

func validate(bucket Bucket, path string) error {
	switch bucket {
	case BucketPayloads, BucketConversations:
	default:
		return fmt.Errorf("unknown blob bucket %q", bucket)
	}

	if path == "" {
		return fmt.Errorf("blob path is empty")
	}
	if strings.HasPrefix(path, "/") || strings.HasSuffix(path, "/") {
		return fmt.Errorf("blob path %q has a leading or trailing slash", path)
	}
	if strings.Contains(path, "\\") {
		return fmt.Errorf("blob path %q contains a backslash", path)
	}

	for _, element := range strings.Split(path, "/") {
		switch element {
		case "":
			return fmt.Errorf("blob path %q has an empty element", path)
		case ".", "..":
			return fmt.Errorf("blob path %q contains traversal element %q", path, element)
		}
	}

	return nil
}
