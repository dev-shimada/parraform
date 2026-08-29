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

func testAWSConfig(endpoint string) aws.Config {
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

	client := s3.NewFromConfig(testAWSConfig(srv.URL), func(o *s3.Options) {
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

	client := s3.NewFromConfig(testAWSConfig(srv.URL), func(o *s3.Options) {
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

	client := dynamodb.NewFromConfig(testAWSConfig(srv.URL), func(o *dynamodb.Options) {
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

	client := dynamodb.NewFromConfig(testAWSConfig(srv.URL), func(o *dynamodb.Options) {
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
