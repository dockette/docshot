package server

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/dockette/docshot/internal/apierr"
	"github.com/dockette/docshot/internal/render"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

func (s *Server) pathInput(p string) (input, error) {
	real, st, err := s.resolvePath(p)
	if err != nil {
		return input{}, err
	}
	rel, _ := filepath.Rel(s.cfg.root, real)
	return input{
		name:         filepath.Base(real),
		storeBase:    filepath.ToSlash(rel),
		version:      "path:" + real + ":" + itoa64(st.Size()) + ":" + itoa64(st.ModTime().UnixNano()),
		cacheControl: s.cfg.cacheControl,
		load: func(context.Context) ([]byte, error) {
			b, err := os.ReadFile(real)
			if err != nil {
				return nil, apierr.New(http.StatusInternalServerError, "reading %s: %v", p, err)
			}
			return b, nil
		},
	}, nil
}

// resolvePath confines p to DOCSHOT_ROOT, symlinks included.
func (s *Server) resolvePath(p string) (string, os.FileInfo, error) {
	if s.cfg.root == "" {
		return "", nil, apierr.New(http.StatusNotFound, "path source disabled: set DOCSHOT_ROOT and mount a directory")
	}
	rootReal, err := filepath.EvalSymlinks(s.cfg.root)
	if err != nil {
		return "", nil, apierr.New(http.StatusInternalServerError, "DOCSHOT_ROOT: %v", err)
	}
	// Clean against "/" first so "../../etc/passwd" collapses to "/etc/passwd"
	// under the root, then resolve symlinks and check we are still inside.
	real, err := filepath.EvalSymlinks(filepath.Join(rootReal, filepath.Clean("/"+p)))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil, apierr.New(http.StatusNotFound, "no such file: %s", p)
		}
		return "", nil, apierr.New(http.StatusBadRequest, "path: %v", err)
	}
	if real != rootReal && !strings.HasPrefix(real, rootReal+string(filepath.Separator)) {
		return "", nil, apierr.New(http.StatusForbidden, "path escapes DOCSHOT_ROOT")
	}
	st, err := os.Stat(real)
	if err != nil || !st.Mode().IsRegular() {
		return "", nil, apierr.New(http.StatusNotFound, "not a regular file: %s", p)
	}
	if st.Size() > s.cfg.maxBytes {
		return "", nil, apierr.New(http.StatusRequestEntityTooLarge, "file larger than %d bytes", s.cfg.maxBytes)
	}
	return real, st, nil
}

func newS3(cfg Config) (*minio.Client, error) {
	if cfg.s3Endpoint == "" {
		return nil, nil
	}
	return minio.New(cfg.s3Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.s3AccessKey, cfg.s3SecretKey, ""),
		Secure: cfg.s3SSL,
		Region: cfg.s3Region,
	})
}

type s3Ref struct{ bucket, key, etag string }

func (s *Server) s3Input(ctx context.Context, ref string) (input, error) {
	if s.s3 == nil {
		return input{}, apierr.New(http.StatusNotFound, "s3 source disabled: set DOCSHOT_S3_ENDPOINT and credentials")
	}
	bucket, key, ok := strings.Cut(strings.TrimPrefix(ref, "s3://"), "/")
	if !ok || bucket == "" || key == "" {
		return input{}, apierr.New(http.StatusBadRequest, "s3 must be bucket/key")
	}
	if !s.bucketAllowed(bucket) {
		return input{}, apierr.New(http.StatusForbidden, "bucket %q not in DOCSHOT_S3_BUCKETS", bucket)
	}
	st, err := s.s3.StatObject(ctx, bucket, key, minio.StatObjectOptions{})
	if err != nil {
		return input{}, s3Err(err, ref)
	}
	if st.Size > s.cfg.maxBytes {
		return input{}, apierr.New(http.StatusRequestEntityTooLarge, "object larger than %d bytes", s.cfg.maxBytes)
	}
	src := &s3Ref{bucket: bucket, key: key, etag: strings.Trim(st.ETag, `"`)}
	return input{
		name:         path.Base(key),
		s3:           src,
		version:      "s3:" + bucket + "/" + key + ":" + src.etag,
		cacheControl: s.cfg.cacheControl,
		load: func(ctx context.Context) ([]byte, error) {
			obj, err := s.s3.GetObject(ctx, bucket, key, minio.GetObjectOptions{})
			if err != nil {
				return nil, s3Err(err, ref)
			}
			defer obj.Close()
			b, err := io.ReadAll(io.LimitReader(obj, s.cfg.maxBytes+1))
			if err != nil {
				return nil, s3Err(err, ref)
			}
			return b, nil
		},
	}, nil
}

func (s *Server) bucketAllowed(b string) bool {
	return len(s.cfg.s3Buckets) == 0 || s.cfg.s3Buckets[b]
}

// Stored results carry x-amz-meta-docshot-source (source ETag or content hash)
// and are reused only while it matches.

var storeStrategies = map[string]bool{"append": true, "replace": true, "suffix": true, "hash": true}

const metaSource = "docshot-source"

type storeTarget struct {
	bucket, key string
	tag         string // the source revision the object must carry to count as fresh
}

func (t storeTarget) String() string { return "s3://" + t.bucket + "/" + t.key }

// storeTarget works out where a result goes. content may be "" before loading;
// then only name-based S3 sources resolve (ok=false: retry after loading).
func (s *Server) storeTarget(in input, o options, content string) (storeTarget, bool, error) {
	if s.s3 == nil {
		return storeTarget{}, false, apierr.New(http.StatusBadRequest, "store=s3 needs DOCSHOT_S3_ENDPOINT and credentials")
	}
	var t storeTarget
	base := ""
	switch {
	case in.s3 != nil:
		t.bucket, base, t.tag = in.s3.bucket, in.s3.key, "etag:"+in.s3.etag
	default:
		if s.cfg.storeBucket == "" {
			return storeTarget{}, false, apierr.New(http.StatusBadRequest, "store=s3 for a non-S3 source needs DOCSHOT_STORE_BUCKET")
		}
		t.bucket = s.cfg.storeBucket
		if in.storeBase != "" {
			base = s.cfg.storePrefix + strings.TrimPrefix(in.storeBase, "/")
		}
		if content == "" {
			return storeTarget{}, false, nil
		}
		t.tag = "sha:" + content
	}
	if !s.bucketAllowed(t.bucket) {
		return storeTarget{}, false, apierr.New(http.StatusForbidden, "bucket %q not in DOCSHOT_S3_BUCKETS", t.bucket)
	}

	strategy := o.storeStrategy
	if base == "" {
		strategy = "hash" // an upload without a filename has nothing else to go by
	}
	ext := outputExt(o.Options)
	op := ""
	if o.Op != render.OpPage {
		op = "." + o.Op
	}
	v := variant(o.Options, s.cfg.defaultDPI)
	switch strategy {
	case "hash":
		if content == "" {
			return storeTarget{}, false, nil
		}
		t.key = s.cfg.storePrefix + "_hash/" + content[:2] + "/" + content + op + v + "." + ext
		t.tag = "sha:" + content // same content from any source → same object
	case "replace":
		t.key = stem(base) + op + v + "." + ext
	case "suffix":
		name := map[string]string{render.OpPage: "_preview", render.OpGrid: "_grid", render.OpPages: "_pages"}[o.Op]
		t.key = stem(base) + name + v + "." + ext
	default: // append
		t.key = base + op + v + "." + ext
	}
	return t, true, nil
}

func stem(key string) string {
	if e := path.Ext(key); e != "" && !strings.Contains(e, "/") {
		return strings.TrimSuffix(key, e)
	}
	return key
}

// fetchStored returns the stored result if it is fresh.
func (s *Server) fetchStored(ctx context.Context, t storeTarget) ([]byte, bool) {
	st, err := s.s3.StatObject(ctx, t.bucket, t.key, minio.StatObjectOptions{})
	if err != nil || metaGet(st.UserMetadata, metaSource) != t.tag {
		return nil, false // missing, or made from an older revision of the source
	}
	obj, err := s.s3.GetObject(ctx, t.bucket, t.key, minio.GetObjectOptions{})
	if err != nil {
		return nil, false
	}
	defer obj.Close()
	b, err := io.ReadAll(obj)
	return b, err == nil
}

func (s *Server) putStored(ctx context.Context, t storeTarget, data []byte, contentType string) error {
	_, err := s.s3.PutObject(ctx, t.bucket, t.key, bytes.NewReader(data), int64(len(data)), minio.PutObjectOptions{
		ContentType:  contentType,
		UserMetadata: map[string]string{metaSource: t.tag},
	})
	return err
}

func metaGet(m map[string]string, key string) string {
	for k, v := range m {
		if strings.EqualFold(k, key) {
			return v
		}
	}
	return ""
}

func s3Err(err error, ref string) error {
	switch minio.ToErrorResponse(err).Code {
	case "NoSuchKey", "NoSuchBucket":
		return apierr.New(http.StatusNotFound, "no such object: %s", ref)
	case "AccessDenied":
		return apierr.New(http.StatusForbidden, "s3 access denied: %s", ref)
	}
	return apierr.New(http.StatusBadGateway, "s3: %v", err)
}
