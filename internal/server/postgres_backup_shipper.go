// Off-host backup shipper: upload local .sql.gz files to S3-compatible storage.
//
// Works against AWS S3, MinIO, R2, B2 — anything that speaks the S3 API. The
// only difference is the endpoint URL and whether path-style addressing is
// required (MinIO/R2 typically yes; AWS no).
package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"

	"github.com/darvell/blob/internal/api"
)

// s3Client builds an aws-sdk-go-v2 s3.Client from a backup config. The
// "force path style" hint lives on the s3 client (not the AWS config) in v2.
func s3Client(c *api.PostgresBackupConfig) (*s3.Client, error) {
	region := c.S3Region
	if region == "" {
		region = "us-east-1"
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(context.Background(),
		awsconfig.WithRegion(region),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(c.S3AccessKeyID, c.S3SecretAccessKey, "")),
	)
	if err != nil {
		return nil, err
	}
	opts := []func(*s3.Options){}
	if c.S3Endpoint != "" {
		opts = append(opts, func(o *s3.Options) {
			o.BaseEndpoint = aws.String(c.S3Endpoint)
		})
	}
	if c.S3UsePathStyle || c.S3Endpoint != "" {
		// Default to path style whenever a custom endpoint is set —
		// MinIO/R2/B2 require it; AWS still accepts it.
		opts = append(opts, func(o *s3.Options) {
			o.UsePathStyle = true
		})
	}
	return s3.NewFromConfig(awsCfg, opts...), nil
}

func (s *Server) testBackupDestination(ctx context.Context, c *api.PostgresBackupConfig) error {
	if c.S3Bucket == "" {
		return errors.New("s3_bucket required")
	}
	cli, err := s3Client(c)
	if err != nil {
		return err
	}
	tctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	_, err = cli.HeadBucket(tctx, &s3.HeadBucketInput{Bucket: aws.String(c.S3Bucket)})
	if err != nil {
		return err
	}
	return nil
}

// shipBackup uploads the local backup file (and a .sha256 sidecar) to the
// configured destination. Retries up to 3 times with exponential backoff.
// Returns the s3 URL and the hex sha256.
func (s *Server) shipBackup(ctx context.Context, c *api.PostgresBackupConfig, localPath string) (remoteURL, sha256hex string, err error) {
	cli, err := s3Client(c)
	if err != nil {
		return "", "", err
	}
	hash, err := sha256File(localPath)
	if err != nil {
		return "", "", err
	}
	hashHex := hex.EncodeToString(hash)
	filename := lastPathComponent(localPath)
	key := c.S3Prefix + filename

	var lastErr error
	for attempt := 1; attempt <= 3; attempt++ {
		if err := putFile(ctx, cli, c.S3Bucket, key, localPath); err != nil {
			lastErr = err
			time.Sleep(time.Duration(attempt*attempt) * time.Second)
			continue
		}
		// Sidecar checksum so a remote-only restore can verify.
		if err := putBytes(ctx, cli, c.S3Bucket, key+".sha256", []byte(hashHex+"  "+filename+"\n")); err != nil {
			lastErr = err
			time.Sleep(time.Duration(attempt*attempt) * time.Second)
			continue
		}
		lastErr = nil
		break
	}
	if lastErr != nil {
		return "", "", lastErr
	}
	return fmt.Sprintf("s3://%s/%s", c.S3Bucket, key), hashHex, nil
}

func putFile(ctx context.Context, cli *s3.Client, bucket, key, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	_, err = cli.PutObject(ctx, &s3.PutObjectInput{
		Bucket:        aws.String(bucket),
		Key:           aws.String(key),
		Body:          f,
		ContentLength: aws.Int64(st.Size()),
		ContentType:   aws.String("application/gzip"),
	})
	return err
}

func putBytes(ctx context.Context, cli *s3.Client, bucket, key string, data []byte) error {
	_, err := cli.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(bucket),
		Key:         aws.String(key),
		Body:        bytes.NewReader(data),
		ContentType: aws.String("text/plain"),
	})
	return err
}

func sha256File(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return nil, err
	}
	return h.Sum(nil), nil
}

func lastPathComponent(p string) string {
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[i+1:]
	}
	return p
}

// listRemoteBackups returns the filenames present under the config's S3 prefix.
// Returns just basenames (without the prefix) for easier joining with local listings.
func (s *Server) listRemoteBackups(ctx context.Context, c *api.PostgresBackupConfig) (map[string]int64, error) {
	cli, err := s3Client(c)
	if err != nil {
		return nil, err
	}
	out := map[string]int64{}
	var token *string
	for {
		resp, err := cli.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
			Bucket:            aws.String(c.S3Bucket),
			Prefix:            aws.String(c.S3Prefix),
			ContinuationToken: token,
		})
		if err != nil {
			return nil, err
		}
		for _, o := range resp.Contents {
			key := aws.ToString(o.Key)
			if !strings.HasSuffix(key, ".sql.gz") {
				continue
			}
			name := strings.TrimPrefix(key, c.S3Prefix)
			size := int64(0)
			if o.Size != nil {
				size = *o.Size
			}
			out[name] = size
		}
		if resp.IsTruncated == nil || !*resp.IsTruncated {
			break
		}
		token = resp.NextContinuationToken
	}
	return out, nil
}

func (s *Server) downloadRemoteBackup(ctx context.Context, c *api.PostgresBackupConfig, key, dest string) error {
	cli, err := s3Client(c)
	if err != nil {
		return err
	}
	resp, err := cli.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(c.S3Bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	out, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, resp.Body)
	return err
}

func (s *Server) deleteRemoteBackup(ctx context.Context, c *api.PostgresBackupConfig, key string) error {
	cli, err := s3Client(c)
	if err != nil {
		return err
	}
	_, err = cli.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(c.S3Bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		var nsk *s3types.NoSuchKey
		if errors.As(err, &nsk) {
			return nil
		}
		return err
	}
	return nil
}

// --- retention math (pure, unit-tested) --------------------------------------

// retentionDecision takes a sorted-newest-first list of backup filenames
// (UTC ISO8601 like "2026-05-03T19-16-02Z.sql.gz") and the daily/weekly/
// monthly retention counts, and returns the set of filenames to KEEP.
//
// Bucketing rule:
//   - "daily"   = one keeper per UTC date, newest in that day wins
//   - "weekly"  = one keeper per ISO year-week, newest in that week wins,
//                 ONLY counting backups taken on Sunday (or the newest of
//                 each calendar week if no Sunday exists in retained set)
//   - "monthly" = one keeper per UTC year-month, newest in that month wins
//
// We keep the union: anything chosen by daily, weekly, or monthly survives.
// Anything not chosen is deletable.
func retentionDecision(names []string, daily, weekly, monthly int) (keep map[string]struct{}) {
	keep = map[string]struct{}{}
	type parsed struct {
		name string
		t    time.Time
	}
	var ps []parsed
	for _, n := range names {
		t, err := parseBackupTime(n)
		if err != nil {
			// Unknown format — keep it (don't punish foreign files).
			keep[n] = struct{}{}
			continue
		}
		ps = append(ps, parsed{n, t})
	}
	// Sort newest first.
	sort.Slice(ps, func(i, j int) bool { return ps[i].t.After(ps[j].t) })

	// Daily: at most `daily` distinct UTC dates.
	seenDay := map[string]bool{}
	dailyCount := 0
	for _, p := range ps {
		if dailyCount >= daily {
			break
		}
		key := p.t.Format("2006-01-02")
		if seenDay[key] {
			continue
		}
		seenDay[key] = true
		keep[p.name] = struct{}{}
		dailyCount++
	}
	// Weekly: at most `weekly` distinct ISO year-weeks.
	seenWeek := map[string]bool{}
	weeklyCount := 0
	for _, p := range ps {
		if weeklyCount >= weekly {
			break
		}
		y, w := p.t.ISOWeek()
		key := fmt.Sprintf("%04d-W%02d", y, w)
		if seenWeek[key] {
			continue
		}
		seenWeek[key] = true
		keep[p.name] = struct{}{}
		weeklyCount++
	}
	// Monthly: at most `monthly` distinct UTC year-months.
	seenMonth := map[string]bool{}
	monthlyCount := 0
	for _, p := range ps {
		if monthlyCount >= monthly {
			break
		}
		key := p.t.Format("2006-01")
		if seenMonth[key] {
			continue
		}
		seenMonth[key] = true
		keep[p.name] = struct{}{}
		monthlyCount++
	}
	return keep
}

// parseBackupTime extracts the UTC timestamp from filenames like
// "2026-05-03T19-16-02Z.sql.gz".
func parseBackupTime(name string) (time.Time, error) {
	stem := strings.TrimSuffix(name, ".sql.gz")
	return time.Parse("2006-01-02T15-04-05Z", stem)
}
