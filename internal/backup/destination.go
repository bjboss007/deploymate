package backup

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"github.com/habibmuhammad/deploymate/internal/config"
)

// Destination is where backup objects live: Cloudflare R2 (via the s3 type)
// in production, a local directory in dev/e2e. Keys are slash-separated
// paths under backups/{service-slug}/ (see objects.go).
type Destination interface {
	Put(ctx context.Context, key string, data []byte) error
	Get(ctx context.Context, key string) ([]byte, error)
	// List returns the objects under prefix (recursive).
	List(ctx context.Context, prefix string) ([]Object, error)
	// Delete removes an object; deleting a missing object succeeds
	// (prune races and re-runs are harmless).
	Delete(ctx context.Context, key string) error
}

// Object is one stored backup object as the listing sees it.
type Object struct {
	Key          string
	Size         int64
	LastModified time.Time
	SHA          string // sha256 of the encrypted blob, from the sibling meta ("" if unreadable)
	TakenAt      time.Time
}

func (o *Object) parseTakenAt() time.Time {
	t, err := parseTakenAt(o.Key)
	if err != nil {
		return o.LastModified
	}
	return t
}

// NewDestinations materializes the configured destination blocks: local
// directories need no client; s3 blocks get a minio client (one per
// destination, each with its own credentials/bucket).
func NewDestinations(cfgs map[string]config.BackupDestination) (map[string]Destination, error) {
	out := make(map[string]Destination, len(cfgs))
	for id, c := range cfgs {
		switch c.Type {
		case "local":
			if err := os.MkdirAll(c.Dir, 0o700); err != nil {
				return nil, fmt.Errorf("backup destination %s: %w", id, err)
			}
			out[id] = &LocalDestination{root: c.Dir}
		case "s3":
			d, err := newS3Destination(c)
			if err != nil {
				return nil, err
			}
			out[id] = d
		default:
			return nil, fmt.Errorf("backup destination %s: unknown type %q", id, c.Type)
		}
	}
	return out, nil
}

// LocalDestination stores objects as files under root (dev + e2e only —
// never a backup of record).
type LocalDestination struct{ root string }

func (d *LocalDestination) path(key string) string {
	return filepath.Join(d.root, filepath.FromSlash(key))
}

func (d *LocalDestination) Put(_ context.Context, key string, data []byte) error {
	p := d.path(key)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	return os.WriteFile(p, data, 0o600)
}

func (d *LocalDestination) Get(_ context.Context, key string) ([]byte, error) {
	return os.ReadFile(d.path(key))
}

func (d *LocalDestination) List(_ context.Context, prefix string) ([]Object, error) {
	var out []Object
	err := filepath.WalkDir(d.root, func(p string, de fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if de.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(d.root, p)
		if err != nil {
			return err
		}
		key := filepath.ToSlash(rel)
		if !strings.HasPrefix(key, prefix) {
			return nil
		}
		info, err := de.Info()
		if err != nil {
			return err
		}
		out = append(out, Object{Key: key, Size: info.Size(), LastModified: info.ModTime().UTC()})
		return nil
	})
	return out, err
}

func (d *LocalDestination) Delete(_ context.Context, key string) error {
	err := os.Remove(d.path(key))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// S3Destination stores objects in an S3-compatible bucket (Cloudflare R2:
// endpoint https://<account>.r2.cloudflarestorage.com, path-style access,
// SigV4). R2's server-side encryption is belt-and-suspenders — blobs are
// already client-side encrypted before upload.
type S3Destination struct {
	client *minio.Client
	bucket string
}

func newS3Destination(c config.BackupDestination) (*S3Destination, error) {
	u, err := url.Parse(c.Endpoint)
	if err != nil {
		return nil, fmt.Errorf("backup destination %s: bad endpoint: %w", c.ID, err)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("backup destination %s: endpoint %q has no host", c.ID, c.Endpoint)
	}
	cli, err := minio.New(u.Host, &minio.Options{
		Creds:  credentials.NewStaticV4(c.AccessKey, c.SecretKey, ""),
		Secure: u.Scheme == "https",
	})
	if err != nil {
		return nil, fmt.Errorf("backup destination %s: %w", c.ID, err)
	}
	return &S3Destination{client: cli, bucket: c.Bucket}, nil
}

func (d *S3Destination) Put(ctx context.Context, key string, data []byte) error {
	_, err := d.client.PutObject(ctx, d.bucket, key, bytes.NewReader(data), int64(len(data)), minio.PutObjectOptions{ContentType: "application/octet-stream"})
	if err != nil {
		return fmt.Errorf("s3 put %s: %w", key, err)
	}
	return nil
}

func (d *S3Destination) Get(ctx context.Context, key string) ([]byte, error) {
	obj, err := d.client.GetObject(ctx, d.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, err
	}
	defer obj.Close()
	if _, err := obj.Stat(); err != nil {
		return nil, err
	}
	return io.ReadAll(obj)
}

func (d *S3Destination) List(ctx context.Context, prefix string) ([]Object, error) {
	var out []Object
	for info := range d.client.ListObjects(ctx, d.bucket, minio.ListObjectsOptions{Prefix: prefix, Recursive: true}) {
		if info.Err != nil {
			return nil, fmt.Errorf("s3 list %s: %w", prefix, info.Err)
		}
		out = append(out, Object{Key: info.Key, Size: info.Size, LastModified: info.LastModified})
	}
	return out, nil
}

func (d *S3Destination) Delete(ctx context.Context, key string) error {
	err := d.client.RemoveObject(ctx, d.bucket, key, minio.RemoveObjectOptions{})
	if err != nil {
		return fmt.Errorf("s3 delete %s: %w", key, err)
	}
	return nil
}
