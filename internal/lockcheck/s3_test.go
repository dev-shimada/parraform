package lockcheck

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	awscreds "github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
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
		_, _ = fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?>
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
		_, _ = fmt.Fprint(w, `{"ID":"abc-123","Who":"runner@github-actions","Version":"1.15.8"}`)
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
		_, _ = fmt.Fprint(w, `{}`)
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
		_, _ = fmt.Fprint(w, `{
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

func TestPeekDynamoDBLock_UsesConsistentRead(t *testing.T) {
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		body = string(b)
		w.Header().Set("Content-Type", "application/x-amz-json-1.0")
		_, _ = fmt.Fprint(w, `{}`)
	}))
	defer srv.Close()

	client := dynamodb.NewFromConfig(testAWSConfig(), func(o *dynamodb.Options) {
		o.BaseEndpoint = aws.String(srv.URL)
	})
	if _, _, err := peekDynamoDBLock(context.Background(), client, "tf-locks", "my-bucket", "k"); err != nil {
		t.Fatalf("peekDynamoDBLock() error = %v", err)
	}
	if !strings.Contains(body, `"ConsistentRead":true`) {
		t.Errorf("GetItem request = %s, want ConsistentRead true (an eventually consistent read can miss a just-taken lock)", body)
	}
}

type fakeS3 struct {
	locked bool
	err    error
	calls  int
}

func (f *fakeS3) GetObject(context.Context, *s3.GetObjectInput, ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	if !f.locked {
		return nil, &smithy.GenericAPIError{Code: "NoSuchKey"}
	}
	return &s3.GetObjectOutput{Body: io.NopCloser(strings.NewReader(`{"Who":"s3-holder"}`))}, nil
}

type fakeDDB struct {
	locked bool
	err    error
	calls  int
}

func (f *fakeDDB) GetItem(context.Context, *dynamodb.GetItemInput, ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	if !f.locked {
		return &dynamodb.GetItemOutput{}, nil
	}
	return &dynamodb.GetItemOutput{Item: map[string]ddbtypes.AttributeValue{
		"Info": &ddbtypes.AttributeValueMemberS{Value: `{"Who":"ddb-holder"}`},
	}}, nil
}

func TestPeekS3Locks(t *testing.T) {
	boom := errors.New("AccessDenied")

	tests := []struct {
		name        string
		s3          fakeS3
		ddb         fakeDDB
		useLockfile bool
		table       string
		wantLocked  bool
		wantWho     string
		wantErr     bool
		wantS3Calls int
		wantDDBCall int
	}{
		{name: "lockfile only, free", useLockfile: true, wantS3Calls: 1},
		{name: "lockfile only, held", s3: fakeS3{locked: true}, useLockfile: true, wantLocked: true, wantWho: "s3-holder", wantS3Calls: 1},
		{name: "table only, free", table: "t", wantDDBCall: 1},
		{name: "table only, held", ddb: fakeDDB{locked: true}, table: "t", wantLocked: true, wantWho: "ddb-holder", wantDDBCall: 1},
		{name: "both configured, both free", useLockfile: true, table: "t", wantS3Calls: 1, wantDDBCall: 1},
		{
			// The reviewer's case: no S3 lockfile does not mean no lock.
			name: "both configured, only the DynamoDB lock held", ddb: fakeDDB{locked: true},
			useLockfile: true, table: "t", wantLocked: true, wantWho: "ddb-holder", wantS3Calls: 1, wantDDBCall: 1,
		},
		{
			name: "both configured, S3 lockfile held: DynamoDB not consulted", s3: fakeS3{locked: true},
			useLockfile: true, table: "t", wantLocked: true, wantWho: "s3-holder", wantS3Calls: 1,
		},
		{
			name: "both configured, S3 free but DynamoDB unreadable is an error, not free", ddb: fakeDDB{err: boom},
			useLockfile: true, table: "t", wantErr: true, wantS3Calls: 1, wantDDBCall: 1,
		},
		{
			name: "both configured, S3 unreadable is an error without trusting DynamoDB", s3: fakeS3{err: boom}, ddb: fakeDDB{},
			useLockfile: true, table: "t", wantErr: true, wantS3Calls: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info, supported, err := peekS3Locks(context.Background(), &tt.s3, &tt.ddb, "b", "k", tt.useLockfile, tt.table)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if !tt.wantErr {
				if !supported {
					t.Error("supported = false, want true")
				}
				if info.Locked != tt.wantLocked || info.Who != tt.wantWho {
					t.Errorf("info = %+v, want Locked=%v Who=%q", info, tt.wantLocked, tt.wantWho)
				}
			}
			if tt.s3.calls != tt.wantS3Calls || tt.ddb.calls != tt.wantDDBCall {
				t.Errorf("calls: s3=%d ddb=%d, want s3=%d ddb=%d", tt.s3.calls, tt.ddb.calls, tt.wantS3Calls, tt.wantDDBCall)
			}
		})
	}
}
