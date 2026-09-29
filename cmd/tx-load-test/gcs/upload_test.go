package gcs

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseDestination(t *testing.T) {
	cases := []struct {
		name       string
		url        string
		localPath  string
		wantBucket string
		wantObject string
		wantErr    bool
	}{
		{"bare bucket appends basename", "gs://metrics", "/data/run.ndjson", "metrics", "run.ndjson", false},
		{"bucket with trailing slash", "gs://metrics/", "/data/run.ndjson", "metrics", "run.ndjson", false},
		{"prefix appends basename", "gs://metrics/weekly/", "/data/run.ndjson", "metrics", "weekly/run.ndjson", false},
		{"exact object name", "gs://metrics/weekly/custom.ndjson", "/data/run.ndjson", "metrics", "weekly/custom.ndjson", false},
		{"deep prefix", "gs://m/a/b/c/", "out.ndjson", "m", "a/b/c/out.ndjson", false},
		{"missing scheme", "s3://metrics/x", "f", "", "", true},
		{"empty bucket", "gs:///weekly/", "f", "", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dest, err := parseDestination(tc.url, tc.localPath)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.wantBucket, dest.bucket)
			require.Equal(t, tc.wantObject, dest.object)
		})
	}
}

// TestUploadAgainstEmulatorEndpoint drives the real client through a local
// HTTP server via STORAGE_EMULATOR_HOST (which also disables authentication),
// verifying the uploaded bytes, target bucket/object, and returned URL.
func TestUploadAgainstEmulatorEndpoint(t *testing.T) {
	var gotPath, gotQuery string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/upload/") {
			http.NotFound(w, r)
			return
		}
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		gotBody = body
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"name":"weekly/metrics.ndjson","bucket":"loadtest-metrics"}`))
	}))
	defer srv.Close()
	t.Setenv("STORAGE_EMULATOR_HOST", strings.TrimPrefix(srv.URL, "http://"))

	local := filepath.Join(t.TempDir(), "metrics.ndjson")
	content := []byte(`{"record_type":"summary","on_chain_included":123}` + "\n")
	require.NoError(t, os.WriteFile(local, content, 0o600))

	objectURL, err := Upload(context.Background(), "gs://loadtest-metrics/weekly/", local)
	require.NoError(t, err)
	require.Equal(t, "gs://loadtest-metrics/weekly/metrics.ndjson", objectURL)

	require.Contains(t, gotPath, "/b/loadtest-metrics/o")
	require.Contains(t, gotQuery, "name=weekly%2Fmetrics.ndjson")
	// The upload is multipart (JSON metadata + payload); the payload must
	// contain our exact NDJSON bytes.
	require.Contains(t, string(gotBody), string(content))
}

func TestUploadMissingLocalFile(t *testing.T) {
	_, err := Upload(context.Background(), "gs://bucket/", filepath.Join(t.TempDir(), "nope.ndjson"))
	require.Error(t, err)
}

func TestUploadBadURL(t *testing.T) {
	local := filepath.Join(t.TempDir(), "m.ndjson")
	require.NoError(t, os.WriteFile(local, []byte("{}\n"), 0o600))
	_, err := Upload(context.Background(), "http://not-gcs/", local)
	require.Error(t, err)
	require.Contains(t, err.Error(), "gs://")
}
