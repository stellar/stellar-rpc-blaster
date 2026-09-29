// Package gcs uploads benchmark output files to Google Cloud Storage.
//
// Authentication uses Application Default Credentials: a mounted service
// account key via GOOGLE_APPLICATION_CREDENTIALS, GKE workload identity, or
// the GCE metadata server all work without tool configuration.
package gcs

import (
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
	"time"

	"cloud.google.com/go/storage"
)

// uploadTimeout bounds a single object upload. Metrics files are small
// (KBs); this is generous headroom for slow first-token auth flows.
const uploadTimeout = 2 * time.Minute

// destination is a parsed gs:// URL.
type destination struct {
	bucket string
	object string
}

// parseDestination validates a gs://bucket[/prefix[/]] URL and resolves the
// final object name for localPath. A URL ending in "/" (or a bare bucket) is
// treated as a prefix and the local file's basename is appended -- with the
// default timestamped metrics filenames this yields a unique object per run.
// Otherwise the URL names the object exactly.
func parseDestination(gcsURL, localPath string) (destination, error) {
	rest, ok := strings.CutPrefix(gcsURL, "gs://")
	if !ok {
		return destination{}, fmt.Errorf("destination %q must start with gs://", gcsURL)
	}
	bucket, object, _ := strings.Cut(rest, "/")
	if bucket == "" {
		return destination{}, fmt.Errorf("destination %q is missing a bucket name", gcsURL)
	}
	if object == "" || strings.HasSuffix(object, "/") {
		object = object + path.Base(localPath)
	}
	return destination{bucket: bucket, object: object}, nil
}

// Upload copies localPath to the gs:// destination and returns the final
// object URL. The context is bounded by uploadTimeout on top of any caller
// deadline.
func Upload(ctx context.Context, gcsURL, localPath string) (string, error) {
	dest, err := parseDestination(gcsURL, localPath)
	if err != nil {
		return "", err
	}

	f, err := os.Open(localPath)
	if err != nil {
		return "", fmt.Errorf("open %s: %w", localPath, err)
	}
	defer f.Close()

	ctx, cancel := context.WithTimeout(ctx, uploadTimeout)
	defer cancel()

	client, err := storage.NewClient(ctx)
	if err != nil {
		return "", fmt.Errorf("create GCS client: %w", err)
	}
	defer client.Close()

	w := client.Bucket(dest.bucket).Object(dest.object).NewWriter(ctx)
	w.ContentType = "application/x-ndjson"
	if _, err := io.Copy(w, f); err != nil {
		_ = w.Close()
		return "", fmt.Errorf("upload to gs://%s/%s: %w", dest.bucket, dest.object, err)
	}
	// Close finalizes the upload; errors here mean the object was not written.
	if err := w.Close(); err != nil {
		return "", fmt.Errorf("finalize gs://%s/%s: %w", dest.bucket, dest.object, err)
	}
	return fmt.Sprintf("gs://%s/%s", dest.bucket, dest.object), nil
}
