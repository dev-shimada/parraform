//go:build integration

package lockcheck

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// ministackImage is pinned by digest (unlike a floating :latest tag) so a
// new ministack release can't change this test's behavior without a commit
// in this repo bumping it deliberately.
const ministackImage = "ministackorg/ministack@sha256:d865b1e43b0b1a7e6f246e1a46fb8a0a2fa04e2a94c64740c8ff454748040726"

// This file exercises s3Checker.Peek() against a real S3/DynamoDB-compatible
// server (ministack, https://github.com/ministackorg/ministack -- a free,
// open-source local AWS emulator), rather than only against httptest fakes
// of the wire format. It's gated behind the "integration" build tag, so
// `go test ./...` never needs docker; run it explicitly with:
//
//	go test -tags=integration ./internal/lockcheck/... -run TestS3Integration -v
//
// Locally, a missing docker or a failed container start just skips the
// test. In CI, set PARRAFORM_REQUIRE_DOCKER=1 so the same conditions fail
// the build instead -- a skipped test still exits 0, and a CI job that can
// only ever pass or silently skip isn't proving anything.
//
// This closes the gap the rest of this package's tests leave open: the
// config-to-identifier math (s3LockTarget) and the response-interpretation
// logic (peekS3Lockfile/peekDynamoDBLock) are both unit-tested already, but
// nothing before this exercised loadAWSConfig's real client construction --
// including AWS_ENDPOINT_URL resolution and the SDK's default virtual/path
// style negotiation -- against an actual server.

// startMinistack starts a ministack container exposing S3 and DynamoDB on
// one emulated endpoint and returns that endpoint's base URL (e.g.
// "http://192.168.1.220:32769"). The container is force-removed via
// t.Cleanup. If docker isn't installed or the container fails to start,
// this skips the test unless PARRAFORM_REQUIRE_DOCKER is set, in which case
// it fails it instead.
func startMinistack(t *testing.T) string {
	t.Helper()

	// In CI, PARRAFORM_REQUIRE_DOCKER=1 turns a missing docker/failed
	// container start into a hard failure instead of a silent skip -- a
	// skipped test still exits 0, so without this the integration job could
	// stop exercising anything and nobody would notice.
	requireDocker := os.Getenv("PARRAFORM_REQUIRE_DOCKER") != ""
	skipOrFail := func(format string, args ...any) {
		if requireDocker {
			t.Fatalf(format, args...)
		}
		t.Skipf(format, args...)
	}

	if _, err := exec.LookPath("docker"); err != nil {
		skipOrFail("docker not available, skipping ministack integration test")
	}

	name := fmt.Sprintf("parraform-ministack-%d", time.Now().UnixNano())
	runCmd := exec.Command("docker", "run", "-d", "--rm", "--name", name, "-P", ministackImage)
	var stderr bytes.Buffer
	runCmd.Stderr = &stderr
	if err := runCmd.Run(); err != nil {
		skipOrFail("failed to start ministack container (docker run: %v): %s", err, stderr.String())
	}
	t.Cleanup(func() {
		_ = exec.Command("docker", "rm", "-f", name).Run()
	})

	port, err := ministackHostPort(name)
	if err != nil {
		t.Fatalf("resolving ministack port: %v", err)
	}
	host := dockerHost(t)
	endpoint := fmt.Sprintf("http://%s:%s", host, port)

	waitForMinistack(t, endpoint)
	return endpoint
}

// ministackHostPort runs `docker port <name> 4566/tcp` and extracts the
// host-side port ministack's emulated endpoint was published on.
func ministackHostPort(name string) (string, error) {
	out, err := exec.Command("docker", "port", name, "4566/tcp").Output()
	if err != nil {
		return "", err
	}
	// Output looks like "0.0.0.0:32769\n[::]:32769\n"; any line's port works.
	line := strings.SplitN(strings.TrimSpace(string(out)), "\n", 2)[0]
	_, port, ok := strings.Cut(line, ":")
	if !ok || port == "" {
		return "", fmt.Errorf("unexpected `docker port` output: %q", out)
	}
	return strings.TrimPrefix(port, "["), nil
}

// dockerHost resolves the address the docker CLI's published ports are
// actually reachable at. Most setups (including GitHub Actions' ubuntu
// runners) talk to a local unix socket, where published ports land on
// 127.0.0.1; this development environment's docker CLI happens to point at
// a remote daemon over ssh/tcp, where they land on that daemon's own host
// instead. Reading the active context's endpoint (rather than hardcoding
// 127.0.0.1) keeps this test runnable in both.
func dockerHost(t *testing.T) string {
	t.Helper()

	out, err := exec.Command("docker", "context", "inspect", "--format", "{{.Endpoints.docker.Host}}").Output()
	if err != nil {
		t.Logf("docker context inspect failed (%v), defaulting to 127.0.0.1", err)
		return "127.0.0.1"
	}
	raw := strings.TrimSpace(string(out))

	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return "127.0.0.1"
	}
	switch u.Scheme {
	case "ssh", "tcp", "http", "https":
		return u.Hostname()
	default: // "unix", or anything socket-based
		return "127.0.0.1"
	}
}

func waitForMinistack(t *testing.T, endpoint string) {
	t.Helper()

	deadline := time.Now().Add(30 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		resp, err := http.Get(endpoint + "/")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
			lastErr = fmt.Errorf("unexpected status %d", resp.StatusCode)
		} else {
			lastErr = err
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("ministack did not become ready at %s: %v", endpoint, lastErr)
}

// s3TestEnv points the AWS SDK's default credential/endpoint resolution
// (the same config.LoadDefaultConfig loadAWSConfig calls in production) at
// ministack, using nothing but the SDK's own standard environment
// variables -- no test-only hook in production code.
func s3TestEnv(t *testing.T, endpoint string) {
	t.Helper()
	t.Setenv("AWS_ENDPOINT_URL", endpoint)
	t.Setenv("AWS_ACCESS_KEY_ID", "test")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test")
	t.Setenv("AWS_REGION", "us-east-1")
}

func TestS3Integration_NativeLockfile(t *testing.T) {
	endpoint := startMinistack(t)
	s3TestEnv(t, endpoint)
	ctx := context.Background()

	awsCfg, err := loadAWSConfig(ctx, map[string]any{"region": "us-east-1"})
	if err != nil {
		t.Fatalf("loadAWSConfig() error = %v", err)
	}
	client := s3.NewFromConfig(awsCfg)

	const bucket = "parraform-test-lockfile"
	if _, err := client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)}); err != nil {
		t.Fatalf("CreateBucket() error = %v", err)
	}

	checker := s3Checker{}
	cfg := testBackendConfigWithType("s3", map[string]any{
		"bucket":       bucket,
		"key":          "terraform.tfstate",
		"region":       "us-east-1",
		"use_lockfile": true,
	}, "default")

	t.Run("not locked before any lockfile exists", func(t *testing.T) {
		info, supported, err := checker.Peek(ctx, cfg)
		if err != nil {
			t.Fatalf("Peek() error = %v", err)
		}
		if !supported {
			t.Fatal("Peek() supported = false, want true")
		}
		if info.Locked {
			t.Errorf("info.Locked = true, want false")
		}
	})

	t.Run("locked once the lockfile object exists", func(t *testing.T) {
		_, err := client.PutObject(ctx, &s3.PutObjectInput{
			Bucket: aws.String(bucket),
			Key:    aws.String("terraform.tfstate.tflock"),
			Body:   strings.NewReader(`{"ID":"abc-123","Who":"integration-test@ministack"}`),
		})
		if err != nil {
			t.Fatalf("PutObject() error = %v", err)
		}

		info, supported, err := checker.Peek(ctx, cfg)
		if err != nil {
			t.Fatalf("Peek() error = %v", err)
		}
		if !supported {
			t.Fatal("Peek() supported = false, want true")
		}
		if !info.Locked {
			t.Errorf("info.Locked = false, want true")
		}
		if info.Who != "integration-test@ministack" {
			t.Errorf("info.Who = %q, want %q", info.Who, "integration-test@ministack")
		}
	})
}

func TestS3Integration_DynamoDBLegacyLock(t *testing.T) {
	endpoint := startMinistack(t)
	s3TestEnv(t, endpoint)
	ctx := context.Background()

	awsCfg, err := loadAWSConfig(ctx, map[string]any{"region": "us-east-1"})
	if err != nil {
		t.Fatalf("loadAWSConfig() error = %v", err)
	}
	s3Client := s3.NewFromConfig(awsCfg)
	ddbClient := dynamodb.NewFromConfig(awsCfg)

	const bucket = "parraform-test-dynamodb"
	const table = "parraform-test-locks"
	if _, err := s3Client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)}); err != nil {
		t.Fatalf("CreateBucket() error = %v", err)
	}
	_, err = ddbClient.CreateTable(ctx, &dynamodb.CreateTableInput{
		TableName: aws.String(table),
		AttributeDefinitions: []ddbtypes.AttributeDefinition{
			{AttributeName: aws.String("LockID"), AttributeType: ddbtypes.ScalarAttributeTypeS},
		},
		KeySchema: []ddbtypes.KeySchemaElement{
			{AttributeName: aws.String("LockID"), KeyType: ddbtypes.KeyTypeHash},
		},
		BillingMode: ddbtypes.BillingModePayPerRequest,
	})
	if err != nil {
		t.Fatalf("CreateTable() error = %v", err)
	}

	checker := s3Checker{}
	cfg := testBackendConfigWithType("s3", map[string]any{
		"bucket":         bucket,
		"key":            "terraform.tfstate",
		"region":         "us-east-1",
		"dynamodb_table": table,
	}, "staging")

	t.Run("not locked before any lock item exists", func(t *testing.T) {
		info, supported, err := checker.Peek(ctx, cfg)
		if err != nil {
			t.Fatalf("Peek() error = %v", err)
		}
		if !supported {
			t.Fatal("Peek() supported = false, want true")
		}
		if info.Locked {
			t.Errorf("info.Locked = true, want false")
		}
	})

	t.Run("locked once the item exists, workspace-qualified key", func(t *testing.T) {
		// Mirrors workspaceObjectKey's non-default-workspace shape:
		// "env:/<workspace>/<key>".
		lockID := bucket + "/env:/staging/terraform.tfstate"
		_, err := ddbClient.PutItem(ctx, &dynamodb.PutItemInput{
			TableName: aws.String(table),
			Item: map[string]ddbtypes.AttributeValue{
				"LockID": &ddbtypes.AttributeValueMemberS{Value: lockID},
				"Info":   &ddbtypes.AttributeValueMemberS{Value: `{"ID":"abc-123","Who":"integration-test@ministack"}`},
			},
		})
		if err != nil {
			t.Fatalf("PutItem() error = %v", err)
		}

		info, supported, err := checker.Peek(ctx, cfg)
		if err != nil {
			t.Fatalf("Peek() error = %v", err)
		}
		if !supported {
			t.Fatal("Peek() supported = false, want true")
		}
		if !info.Locked {
			t.Errorf("info.Locked = false, want true")
		}
		if info.Who != "integration-test@ministack" {
			t.Errorf("info.Who = %q, want %q", info.Who, "integration-test@ministack")
		}
	})
}
