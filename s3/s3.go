// s3/s3.go - S3-compatible object storage filesystem driver
//
// This package implements the api.Driver interface for S3 and
// S3-compatible backends (AWS S3, MinIO, Cloudflare R2, Backblaze B2
// S3-compatible, Wasabi, ...) via the minio-go v7 client.
//
// S3 is a flat key/value store; "directories" are synthesised via the
// delimiter="/" convention. Mkdir writes an empty `path/` placeholder
// (the same convention the AWS console uses) so that otherwise-empty
// directories still show up in ListDir.

package s3

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/christhomas/go-networkfs/pkg/api"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// Driver type ID - must match dispatcher registry
const DriverTypeID = 7

func init() {
	api.RegisterDriver(DriverTypeID, func() api.Driver {
		return &S3Driver{}
	})
}

// S3Driver implements the Driver interface for S3-compatible storage.
type S3Driver struct {
	connected bool
	client    *minio.Client
	bucket    string
	// prefix is either "" or "something/". Paths from callers are
	// joined onto it: "/a/b" -> prefix + "a/b".
	prefix string
}

// Name returns the driver identifier.
func (d *S3Driver) Name() string {
	return "s3"
}

// Mount establishes an S3 client.
//
// Config keys (strings):
//
//	endpoint          - host[:port] of the S3 service (required).
//	                    e.g. "s3.amazonaws.com", "minio.local:9000",
//	                    "<account>.r2.cloudflarestorage.com".
//	bucket            - bucket name (required)
//	access_key_id     - access key (required)
//	secret_access_key - secret key (required)
//	session_token     - optional temporary STS token
//	region            - AWS region; default "us-east-1"
//	secure            - "true"/"false"; default "true" (HTTPS)
//	prefix            - optional key prefix treated as the filesystem root
//	use_path_style    - "true" forces path-style addressing (MinIO etc.)
func (d *S3Driver) Mount(mountID int, config map[string]string) error {
	endpoint := config["endpoint"]
	bucket := config["bucket"]
	accessKey := config["access_key_id"]
	secretKey := config["secret_access_key"]

	if endpoint == "" || bucket == "" || accessKey == "" || secretKey == "" {
		return &api.DriverError{Code: 10, Message: "s3: endpoint, bucket, access_key_id, and secret_access_key are required"}
	}

	secure := true
	if v := config["secure"]; strings.EqualFold(v, "false") {
		secure = false
	}

	region := config["region"]
	if region == "" {
		region = "us-east-1"
	}

	opts := &minio.Options{
		Creds:  credentials.NewStaticV4(accessKey, secretKey, config["session_token"]),
		Secure: secure,
		Region: region,
	}
	if strings.EqualFold(config["use_path_style"], "true") {
		opts.BucketLookup = minio.BucketLookupPath
	}

	client, err := minio.New(endpoint, opts)
	if err != nil {
		return &api.DriverError{Code: 12, Message: "s3 new client: " + err.Error()}
	}

	ok, err := client.BucketExists(context.Background(), bucket)
	if err != nil {
		return &api.DriverError{Code: 12, Message: "s3 bucket check: " + err.Error()}
	}
	if !ok {
		return &api.DriverError{Code: 12, Message: "s3: bucket not found: " + bucket}
	}

	d.client = client
	d.bucket = bucket
	d.prefix = normalizePrefix(config["prefix"])
	d.connected = true
	return nil
}

// Unmount drops the client. minio-go pools HTTP connections internally;
// letting the *Client go out of scope is enough.
func (d *S3Driver) Unmount(mountID int) error {
	d.client = nil
	d.connected = false
	return nil
}

// Stat retrieves file/directory metadata.
func (d *S3Driver) Stat(mountID int, path string) (api.FileInfo, error) {
	if !d.connected || d.client == nil {
		return api.FileInfo{}, api.ErrNotConnected
	}

	p := normPath(path)
	if p == "/" {
		return api.FileInfo{Name: "", Path: "/", IsDir: true}, nil
	}

	key := d.toKey(p)

	// File?
	if info, err := d.client.StatObject(context.Background(), d.bucket, key, minio.StatObjectOptions{}); err == nil {
		return api.FileInfo{
			Name:    nameFromPath(p),
			Path:    p,
			Size:    info.Size,
			IsDir:   false,
			ModTime: info.LastModified.Unix(),
		}, nil
	}

	// Directory? A key exists under `key/` (either a placeholder or a child).
	dirPrefix := key + "/"
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	for obj := range d.client.ListObjects(ctx, d.bucket, minio.ListObjectsOptions{
		Prefix:    dirPrefix,
		Recursive: false,
		MaxKeys:   1,
	}) {
		if obj.Err != nil {
			return api.FileInfo{}, obj.Err
		}
		return api.FileInfo{
			Name:  nameFromPath(p),
			Path:  p,
			IsDir: true,
		}, nil
	}

	return api.FileInfo{}, api.ErrNotFound
}

// ListDir returns entries in a directory.
// minio-go with Recursive=false uses delimiter="/"; common-prefix
// "directories" arrive as entries whose Key ends in "/".
func (d *S3Driver) ListDir(mountID int, path string) ([]api.FileInfo, error) {
	if !d.connected || d.client == nil {
		return nil, api.ErrNotConnected
	}

	p := normPath(path)
	dirPrefix := d.toKey(p)
	if dirPrefix != "" && !strings.HasSuffix(dirPrefix, "/") {
		dirPrefix += "/"
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	trimmed := strings.TrimRight(p, "/")
	seen := map[string]bool{}
	var out []api.FileInfo

	for obj := range d.client.ListObjects(ctx, d.bucket, minio.ListObjectsOptions{
		Prefix:    dirPrefix,
		Recursive: false,
	}) {
		if obj.Err != nil {
			return nil, obj.Err
		}

		rel := strings.TrimPrefix(obj.Key, dirPrefix)
		if rel == "" {
			continue // directory-self placeholder
		}

		if strings.HasSuffix(obj.Key, "/") {
			name := strings.TrimSuffix(rel, "/")
			if name == "" || seen[name] {
				continue
			}
			seen[name] = true
			out = append(out, api.FileInfo{
				Name:  name,
				Path:  trimmed + "/" + name,
				IsDir: true,
			})
			continue
		}

		if seen[rel] {
			continue
		}
		seen[rel] = true
		out = append(out, api.FileInfo{
			Name:    rel,
			Path:    trimmed + "/" + rel,
			Size:    obj.Size,
			IsDir:   false,
			ModTime: obj.LastModified.Unix(),
		})
	}

	return out, nil
}

// OpenFile returns a streaming reader for the object.
func (d *S3Driver) OpenFile(mountID int, path string) (io.ReadCloser, error) {
	if !d.connected || d.client == nil {
		return nil, api.ErrNotConnected
	}
	key := d.toKey(normPath(path))

	obj, err := d.client.GetObject(context.Background(), d.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, err
	}
	// GetObject is lazy: StatObject surfaces not-found / permission errors
	// before returning the stream so callers see them on OpenFile rather
	// than on the first Read.
	if _, err := obj.Stat(); err != nil {
		obj.Close()
		return nil, err
	}
	return obj, nil
}

// CreateFile returns a buffered writer that PUTs on Close().
// S3 PutObject needs a known length, so we buffer (same pattern as the
// Dropbox/WebDAV drivers).
type s3Writer struct {
	buf    bytes.Buffer
	driver *S3Driver
	path   string
	closed bool
}

func (w *s3Writer) Write(p []byte) (int, error) {
	if w.closed {
		return 0, fmt.Errorf("writer closed")
	}
	return w.buf.Write(p)
}

func (w *s3Writer) Close() error {
	if w.closed {
		return nil
	}
	w.closed = true
	if w.driver == nil || w.driver.client == nil {
		return api.ErrNotConnected
	}

	key := w.driver.toKey(w.path)
	data := w.buf.Bytes()
	_, err := w.driver.client.PutObject(context.Background(), w.driver.bucket, key,
		bytes.NewReader(data), int64(len(data)),
		minio.PutObjectOptions{ContentType: "application/octet-stream"})
	return err
}

func (d *S3Driver) CreateFile(mountID int, path string) (io.WriteCloser, error) {
	if !d.connected || d.client == nil {
		return nil, api.ErrNotConnected
	}
	return &s3Writer{driver: d, path: normPath(path)}, nil
}

// Mkdir creates a zero-byte `path/` placeholder object. This is the
// same convention the S3 console uses so that empty directories persist.
func (d *S3Driver) Mkdir(mountID int, path string) error {
	if !d.connected || d.client == nil {
		return api.ErrNotConnected
	}
	key := d.toKey(normPath(path))
	if !strings.HasSuffix(key, "/") {
		key += "/"
	}
	_, err := d.client.PutObject(context.Background(), d.bucket, key,
		bytes.NewReader(nil), 0,
		minio.PutObjectOptions{ContentType: "application/x-directory"})
	return err
}

// Remove deletes a file or an (empty) directory. Directories are
// recognised via the `path/` placeholder. Non-empty directories return
// an error — matching POSIX rmdir semantics; callers that want rm -r
// should list and delete recursively.
func (d *S3Driver) Remove(mountID int, path string) error {
	if !d.connected || d.client == nil {
		return api.ErrNotConnected
	}

	p := normPath(path)
	key := d.toKey(p)

	// File path.
	if _, err := d.client.StatObject(context.Background(), d.bucket, key, minio.StatObjectOptions{}); err == nil {
		return d.client.RemoveObject(context.Background(), d.bucket, key, minio.RemoveObjectOptions{})
	}

	// Directory: must be empty (ignoring its own placeholder).
	dirPrefix := key + "/"
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	hasChildren := false
	for obj := range d.client.ListObjects(ctx, d.bucket, minio.ListObjectsOptions{
		Prefix:    dirPrefix,
		Recursive: true,
		MaxKeys:   2,
	}) {
		if obj.Err != nil {
			return obj.Err
		}
		if obj.Key == dirPrefix {
			continue
		}
		hasChildren = true
		break
	}
	if hasChildren {
		return &api.DriverError{Code: 30, Message: "s3: directory not empty: " + p}
	}

	return d.client.RemoveObject(context.Background(), d.bucket, dirPrefix, minio.RemoveObjectOptions{})
}

// Rename moves/renames a file or directory. S3 has no native rename:
// for files we copy + delete; for directories we recursively copy every
// object under the source prefix to the destination prefix, then delete.
func (d *S3Driver) Rename(mountID int, oldPath, newPath string) error {
	if !d.connected || d.client == nil {
		return api.ErrNotConnected
	}

	srcKey := d.toKey(normPath(oldPath))
	dstKey := d.toKey(normPath(newPath))

	// File?
	if _, err := d.client.StatObject(context.Background(), d.bucket, srcKey, minio.StatObjectOptions{}); err == nil {
		if _, err := d.client.CopyObject(context.Background(),
			minio.CopyDestOptions{Bucket: d.bucket, Object: dstKey},
			minio.CopySrcOptions{Bucket: d.bucket, Object: srcKey}); err != nil {
			return err
		}
		return d.client.RemoveObject(context.Background(), d.bucket, srcKey, minio.RemoveObjectOptions{})
	}

	// Directory: walk every descendant.
	srcPrefix := srcKey + "/"
	dstPrefix := dstKey + "/"

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var toDelete []string
	for obj := range d.client.ListObjects(ctx, d.bucket, minio.ListObjectsOptions{
		Prefix:    srcPrefix,
		Recursive: true,
	}) {
		if obj.Err != nil {
			return obj.Err
		}
		newKey := dstPrefix + strings.TrimPrefix(obj.Key, srcPrefix)
		if _, err := d.client.CopyObject(context.Background(),
			minio.CopyDestOptions{Bucket: d.bucket, Object: newKey},
			minio.CopySrcOptions{Bucket: d.bucket, Object: obj.Key}); err != nil {
			return err
		}
		toDelete = append(toDelete, obj.Key)
	}

	if len(toDelete) == 0 {
		return api.ErrNotFound
	}

	for _, k := range toDelete {
		if err := d.client.RemoveObject(context.Background(), d.bucket, k, minio.RemoveObjectOptions{}); err != nil {
			return err
		}
	}
	return nil
}

// --- internal helpers ------------------------------------------------------

// toKey maps a driver-facing path ("/foo/bar.txt") to an S3 object key,
// honouring the configured prefix.
func (d *S3Driver) toKey(path string) string {
	p := strings.TrimPrefix(path, "/")
	if d.prefix == "" {
		return p
	}
	return d.prefix + p
}

// normalizePrefix turns user input into either "" or "segment/...".
func normalizePrefix(prefix string) string {
	prefix = strings.Trim(prefix, "/")
	if prefix == "" {
		return ""
	}
	return prefix + "/"
}

func normPath(path string) string {
	if path == "" || path == "/" {
		return "/"
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return strings.TrimRight(path, "/")
}

func nameFromPath(path string) string {
	parts := strings.Split(path, "/")
	for i := len(parts) - 1; i >= 0; i-- {
		if parts[i] != "" {
			return parts[i]
		}
	}
	return ""
}
