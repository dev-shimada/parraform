package lockcheck

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	awscreds "github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

func testAWSConfig() aws.Config {
	return aws.Config{
		Region:      "us-east-1",
		Credentials: awscreds.NewStaticCredentialsProvider("test", "test", ""),
	}
}

func TestPeekS3Lockfile_NotLocked(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?>
<Error><Code>NoSuchKey</Code><Message>The specified key does not exist.</Message><Key>envs/prod/terraform.tfstate.tflock</Key><RequestId>x</RequestId><HostId>x</HostId></Error>`)
	}))
	defer srv.Close()

	client := s3.NewFromConfig(testAWSConfig(), func(o *s3.Options) {
		o.BaseEndpoint = aws.String(srv.URL)
		o.UsePathStyle = true
	})

	info, supported, err := peekS3Lockfile(context.Background(), client, "my-bucket", "envs/prod/terraform.tfstate")
	if err != nil {
		t.Fatalf("peekS3Lockfile() error = %v", err)
	}
	if !supported {
		t.Fatal("peekS3Lockfile() supported = false, want true")
	}
	if info.Locked {
		t.Errorf("info.Locked = true, want false")
	}
}

func TestPeekS3Lockfile_Locked(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"ID":"abc-123","Who":"runner@github-actions","Version":"1.15.8"}`)
	}))
	defer srv.Close()

	client := s3.NewFromConfig(testAWSConfig(), func(o *s3.Options) {
		o.BaseEndpoint = aws.String(srv.URL)
		o.UsePathStyle = true
	})

	info, supported, err := peekS3Lockfile(context.Background(), client, "my-bucket", "envs/prod/terraform.tfstate")
	if err != nil {
		t.Fatalf("peekS3Lockfile() error = %v", err)
	}
	if !supported {
		t.Fatal("peekS3Lockfile() supported = false, want true")
	}
	if !info.Locked {
		t.Errorf("info.Locked = false, want true")
	}
	if info.Who != "runner@github-actions" {
		t.Errorf("info.Who = %q, want %q", info.Who, "runner@github-actions")
	}
}

func TestPeekDynamoDBLock_NotLocked(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-amz-json-1.0")
		fmt.Fprint(w, `{}`)
	}))
	defer srv.Close()

	client := dynamodb.NewFromConfig(testAWSConfig(), func(o *dynamodb.Options) {
		o.BaseEndpoint = aws.String(srv.URL)
	})

	info, supported, err := peekDynamoDBLock(context.Background(), client, "tf-locks", "my-bucket", "envs/prod/terraform.tfstate")
	if err != nil {
		t.Fatalf("peekDynamoDBLock() error = %v", err)
	}
	if !supported {
		t.Fatal("peekDynamoDBLock() supported = false, want true")
	}
	if info.Locked {
		t.Errorf("info.Locked = true, want false")
	}
}

func TestPeekDynamoDBLock_Locked(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-amz-json-1.0")
		fmt.Fprint(w, `{
			"Item": {
				"LockID": {"S": "my-bucket/envs/prod/terraform.tfstate"},
				"Info": {"S": "{\"ID\":\"abc-123\",\"Who\":\"runner@github-actions\"}"}
			}
		}`)
	}))
	defer srv.Close()

	client := dynamodb.NewFromConfig(testAWSConfig(), func(o *dynamodb.Options) {
		o.BaseEndpoint = aws.String(srv.URL)
	})

	info, supported, err := peekDynamoDBLock(context.Background(), client, "tf-locks", "my-bucket", "envs/prod/terraform.tfstate")
	if err != nil {
		t.Fatalf("peekDynamoDBLock() error = %v", err)
	}
	if !supported {
		t.Fatal("peekDynamoDBLock() supported = false, want true")
	}
	if !info.Locked {
		t.Errorf("info.Locked = false, want true")
	}
	if info.Who != "runner@github-actions" {
		t.Errorf("info.Who = %q, want %q", info.Who, "runner@github-actions")
	}
}

func TestS3Checker_Peek_NoLockingConfigured(t *testing.T) {
	c := s3Checker{}
	cfg := testBackendConfig(map[string]any{
		"bucket": "my-bucket",
		"key":    "envs/prod/terraform.tfstate",
		"region": "us-east-1",
	})
	_, supported, err := c.Peek(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Peek() error = %v", err)
	}
	if supported {
		t.Error("Peek() supported = true, want false (no dynamodb_table or use_lockfile configured)")
	}
}

func TestWorkspaceObjectKey(t *testing.T) {
	cases := []struct {
		name      string
		key       string
		workspace string
		prefix    string
		want      string
	}{
		{"default workspace unchanged", "path/to/my/key", "default", "", "path/to/my/key"},
		{"empty workspace treated as default", "path/to/my/key", "", "", "path/to/my/key"},
		{"non-default workspace default prefix", "path/to/my/key", "development", "", "env:/development/path/to/my/key"},
		{"non-default workspace custom prefix", "path/to/my/key", "development", "custom-prefix", "custom-prefix/development/path/to/my/key"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := workspaceObjectKey(c.key, c.workspace, c.prefix); got != c.want {
				t.Errorf("workspaceObjectKey(%q, %q, %q) = %q, want %q", c.key, c.workspace, c.prefix, got, c.want)
			}
		})
	}
}

func TestS3LockTarget(t *testing.T) {
	cases := []struct {
		name            string
		cfg             map[string]any
		workspace       string
		wantBucket      string
		wantKey         string
		wantUseLockfile bool
		wantTable       string
		wantOK          bool
	}{
		{
			name:   "missing bucket or key",
			cfg:    map[string]any{"region": "us-east-1"},
			wantOK: false,
		},
		{
			name:   "no locking mechanism configured",
			cfg:    map[string]any{"bucket": "my-bucket", "key": "envs/prod/terraform.tfstate"},
			wantOK: false,
		},
		{
			name:            "native lockfile, default workspace",
			cfg:             map[string]any{"bucket": "my-bucket", "key": "envs/prod/terraform.tfstate", "use_lockfile": true},
			workspace:       "default",
			wantBucket:      "my-bucket",
			wantKey:         "envs/prod/terraform.tfstate",
			wantUseLockfile: true,
			wantOK:          true,
		},
		{
			name:       "dynamodb, non-default workspace, custom prefix",
			cfg:        map[string]any{"bucket": "my-bucket", "key": "terraform.tfstate", "dynamodb_table": "tf-locks", "workspace_key_prefix": "custom"},
			workspace:  "staging",
			wantBucket: "my-bucket",
			wantKey:    "custom/staging/terraform.tfstate",
			wantTable:  "tf-locks",
			wantOK:     true,
		},
		{
			name:            "native lockfile, non-default workspace, default prefix",
			cfg:             map[string]any{"bucket": "my-bucket", "key": "terraform.tfstate", "use_lockfile": true},
			workspace:       "staging",
			wantBucket:      "my-bucket",
			wantKey:         "env:/staging/terraform.tfstate",
			wantUseLockfile: true,
			wantOK:          true,
		},
		{
			name:       "dynamodb, default workspace",
			cfg:        map[string]any{"bucket": "my-bucket", "key": "envs/prod/terraform.tfstate", "dynamodb_table": "tf-locks"},
			workspace:  "default",
			wantBucket: "my-bucket",
			wantKey:    "envs/prod/terraform.tfstate",
			wantTable:  "tf-locks",
			wantOK:     true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg := testBackendConfigWithType("s3", c.cfg, c.workspace)
			bucket, key, useLockfile, table, ok := s3LockTarget(cfg)
			if ok != c.wantOK {
				t.Fatalf("s3LockTarget() ok = %v, want %v", ok, c.wantOK)
			}
			if !ok {
				return
			}
			if bucket != c.wantBucket || key != c.wantKey || useLockfile != c.wantUseLockfile || table != c.wantTable {
				t.Errorf("s3LockTarget() = (%q, %q, %v, %q), want (%q, %q, %v, %q)",
					bucket, key, useLockfile, table, c.wantBucket, c.wantKey, c.wantUseLockfile, c.wantTable)
			}
		})
	}
}

func TestS3Checker_Peek_MissingBucketOrKey(t *testing.T) {
	c := s3Checker{}
	cfg := testBackendConfig(map[string]any{"region": "us-east-1"})
	_, supported, err := c.Peek(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Peek() error = %v", err)
	}
	if supported {
		t.Error("Peek() supported = true, want false (missing bucket/key)")
	}
}
