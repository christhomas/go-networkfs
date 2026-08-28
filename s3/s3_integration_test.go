//go:build s3_integration

// Integration tests for the S3 driver. They need a real S3 service, because
// the driver's behaviour is the behaviour of the protocol: key layout, the
// zero-byte object that stands in for a directory, and the copy-then-delete
// that stands in for a rename. None of that is exercised by testing the path
// helpers alone.
//
// MinIO speaks the protocol and runs in a container, so `make test-s3` gives
// the same server locally and in CI.
//
//	make test-s3
//
// Or by hand, against anything S3-compatible:
//
//	S3_ENDPOINT=127.0.0.1:9000 S3_BUCKET=testbucket \
//	  S3_ACCESS_KEY=minioadmin S3_SECRET_KEY=minioadmin S3_SECURE=false \
//	  go test -tags=s3_integration -v ./s3/...
//
// Without the tag the file is skipped entirely, so a plain `go test ./...`
// never needs a server.

package s3

import (
	"bytes"
	"context"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

func env(key string) string { return os.Getenv(key) }

func requireEnv(t *testing.T) map[string]string {
	t.Helper()
	cfg := map[string]string{
		"endpoint":          env("S3_ENDPOINT"),
		"bucket":            env("S3_BUCKET"),
		"access_key_id":     env("S3_ACCESS_KEY"),
		"secret_access_key": env("S3_SECRET_KEY"),
		"secure":            env("S3_SECURE"),
		"use_path_style":    "true",
		"prefix":            env("S3_PREFIX"),
	}
	var missing []string
	for _, k := range []string{"endpoint", "bucket", "access_key_id", "secret_access_key"} {
		if cfg[k] == "" {
			missing = append(missing, "S3_"+strings.ToUpper(strings.TrimSuffix(k, "_id")))
		}
	}
	if len(missing) > 0 {
		t.Skipf("S3 integration skipped: set %s", strings.Join(missing, ", "))
	}
	return cfg
}

// ensureBucket creates the test bucket if the service does not have it. Mount
// requires the bucket to exist, so this cannot be left to the driver.
func ensureBucket(t *testing.T, cfg map[string]string) {
	t.Helper()
	client, err := minio.New(cfg["endpoint"], &minio.Options{
		Creds:        credentials.NewStaticV4(cfg["access_key_id"], cfg["secret_access_key"], ""),
		Secure:       strings.EqualFold(cfg["secure"], "true"),
		Region:       "us-east-1",
		BucketLookup: minio.BucketLookupPath,
	})
	if err != nil {
		t.Fatalf("minio client: %v", err)
	}
	ctx := context.Background()
	exists, err := client.BucketExists(ctx, cfg["bucket"])
	if err != nil {
		t.Fatalf("bucket check: %v", err)
	}
	if !exists {
		if err := client.MakeBucket(ctx, cfg["bucket"], minio.MakeBucketOptions{}); err != nil {
			t.Fatalf("make bucket: %v", err)
		}
	}
}

func mountIntegration(t *testing.T) *S3Driver {
	t.Helper()
	cfg := requireEnv(t)
	ensureBucket(t, cfg)
	d := &S3Driver{}
	if err := d.Mount(1, cfg); err != nil {
		t.Fatalf("mount: %v", err)
	}
	t.Cleanup(func() { _ = d.Unmount(1) })
	return d
}

// uniqueDir keeps concurrent runs from colliding in a shared bucket.
func uniqueDir(t *testing.T) string {
	return "/gonfs-" + strings.ReplaceAll(t.Name(), "/", "-") + "-" + time.Now().Format("150405.000")
}

func writeFile(t *testing.T, d *S3Driver, path string, body []byte) {
	t.Helper()
	w, err := d.CreateFile(1, path)
	if err != nil {
		t.Fatalf("CreateFile %s: %v", path, err)
	}
	if _, err := w.Write(body); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close %s: %v", path, err)
	}
}

func TestIntegrationMountRejectsMissingBucket(t *testing.T) {
	cfg := requireEnv(t)
	cfg["bucket"] = "definitely-not-a-bucket-" + time.Now().Format("150405.000")

	d := &S3Driver{}
	err := d.Mount(1, cfg)
	if err == nil {
		_ = d.Unmount(1)
		t.Fatal("expected a mount against a missing bucket to fail")
	}
	if !strings.Contains(err.Error(), "bucket") {
		t.Errorf("error does not mention the bucket: %v", err)
	}
}

func TestIntegrationCreateReadRemove(t *testing.T) {
	d := mountIntegration(t)
	dir := uniqueDir(t)
	path := dir + "/hello.txt"
	body := []byte("hello world")

	writeFile(t, d, path, body)

	fi, err := d.Stat(1, path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if fi.IsDir {
		t.Error("file reported as a directory")
	}
	if fi.Size != int64(len(body)) {
		t.Errorf("size %d, want %d", fi.Size, len(body))
	}

	r, err := d.OpenFile(1, path)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	got, err := io.ReadAll(r)
	_ = r.Close()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(got, body) {
		t.Errorf("read %q, want %q", got, body)
	}

	if err := d.Remove(1, path); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := d.Stat(1, path); err == nil {
		t.Error("Stat succeeded after Remove")
	}
}

func TestIntegrationListDir(t *testing.T) {
	d := mountIntegration(t)
	dir := uniqueDir(t)

	for _, name := range []string{"a.txt", "b.txt"} {
		writeFile(t, d, dir+"/"+name, []byte(name))
	}
	if err := d.Mkdir(1, dir+"/sub"); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	t.Cleanup(func() { _ = d.Remove(1, dir) })

	entries, err := d.ListDir(1, dir)
	if err != nil {
		t.Fatalf("ListDir: %v", err)
	}

	seen := map[string]bool{}
	for _, e := range entries {
		seen[e.Name] = e.IsDir
	}
	for _, name := range []string{"a.txt", "b.txt"} {
		if _, ok := seen[name]; !ok {
			t.Errorf("%s missing from listing %v", name, seen)
		}
	}
	if isDir, ok := seen["sub"]; !ok {
		t.Errorf("sub missing from listing %v", seen)
	} else if !isDir {
		t.Error("sub not reported as a directory")
	}
}

// A directory in S3 is a zero-byte object with a trailing slash. Stat has to
// report it as one, since there is nothing else to go on.
func TestIntegrationMkdirStat(t *testing.T) {
	d := mountIntegration(t)
	dir := uniqueDir(t)

	if err := d.Mkdir(1, dir); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	t.Cleanup(func() { _ = d.Remove(1, dir) })

	fi, err := d.Stat(1, dir)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if !fi.IsDir {
		t.Error("directory not reported as one")
	}
}

// Rename is a copy followed by a delete, so both halves need checking.
func TestIntegrationRename(t *testing.T) {
	d := mountIntegration(t)
	dir := uniqueDir(t)
	src, dst := dir+"/before.txt", dir+"/after.txt"
	body := []byte("move me")

	writeFile(t, d, src, body)
	t.Cleanup(func() { _ = d.Remove(1, dst) })

	if err := d.Rename(1, src, dst); err != nil {
		t.Fatalf("Rename: %v", err)
	}

	if _, err := d.Stat(1, src); err == nil {
		t.Error("source still present after rename")
	}

	r, err := d.OpenFile(1, dst)
	if err != nil {
		t.Fatalf("OpenFile after rename: %v", err)
	}
	got, err := io.ReadAll(r)
	_ = r.Close()
	if err != nil {
		t.Fatalf("read after rename: %v", err)
	}
	if !bytes.Equal(got, body) {
		t.Errorf("content changed across rename: %q, want %q", got, body)
	}
}

func TestIntegrationStatMissing(t *testing.T) {
	d := mountIntegration(t)
	if _, err := d.Stat(1, uniqueDir(t)+"/nope.txt"); err == nil {
		t.Error("Stat of a missing key succeeded")
	}
}
