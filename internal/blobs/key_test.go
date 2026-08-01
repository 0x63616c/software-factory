// Package blobs defines durable names for stored blobs.
package blobs

import "testing"

func TestParseKeyRejectsTraversal(t *testing.T) {
	_, err := ParseKey("payloads/../etc/passwd")
	if err == nil {
		t.Fatal("ParseKey() error = nil, want rejection for traversal")
	}
}

func TestParseKeyRejectsUnknownBucket(t *testing.T) {
	_, err := ParseKey("secrets/x")
	if err == nil {
		t.Fatal("ParseKey() error = nil, want rejection for unknown bucket")
	}
}

func TestKeyRoundTrip(t *testing.T) {
	want, err := NewKey(BucketPayloads, "first/second")
	if err != nil {
		t.Fatalf("NewKey() error = %v", err)
	}

	got, err := ParseKey(want.String())
	if err != nil {
		t.Fatalf("ParseKey() error = %v", err)
	}
	if got != want {
		t.Errorf("ParseKey(%q) = %#v, want %#v", want.String(), got, want)
	}
}

func TestParseKeyRejectsMalformed(t *testing.T) {
	testCases := []string{
		"",
		"payloads",
		"payloads/",
		"/payloads/x",
		"payloads//x",
		"payloads/a\\b",
		"payloads/./x",
		"payloads/a/../x",
	}

	for _, testCase := range testCases {
		t.Run(testCase, func(t *testing.T) {
			_, err := ParseKey(testCase)
			if err == nil {
				t.Fatalf("ParseKey(%q) error = nil, want rejection", testCase)
			}
		})
	}
}

func TestNewKeyAppliesTheSameValidation(t *testing.T) {
	testCases := []struct {
		name   string
		bucket Bucket
		path   string
	}{
		{name: "unknown bucket", bucket: "secrets", path: "x"},
		{name: "empty path", bucket: BucketPayloads, path: ""},
		{name: "leading slash", bucket: BucketPayloads, path: "/x"},
		{name: "trailing slash", bucket: BucketPayloads, path: "x/"},
		{name: "empty element", bucket: BucketPayloads, path: "a//b"},
		{name: "backslash", bucket: BucketPayloads, path: "a\\b"},
		{name: "current directory", bucket: BucketPayloads, path: "./x"},
		{name: "parent directory", bucket: BucketPayloads, path: "../etc/passwd"},
		{name: "nested parent directory", bucket: BucketPayloads, path: "a/../x"},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := NewKey(testCase.bucket, testCase.path)
			if err == nil {
				t.Fatalf("NewKey(%q, %q) error = nil, want rejection", testCase.bucket, testCase.path)
			}
		})
	}
}
