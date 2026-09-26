//go:build integration

package main

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// This file black-box tests the actual parraform binary (built fresh, then
// invoked as a subprocess) against a real terraform binary and a real
// S3-compatible server (ministack, https://github.com/ministackorg/ministack),
// closing a gap the rest of the test suite leaves open: main_test.go only
// unit-tests peekWithTimeout and decideLockAction in isolation, so nothing
// before this exercised checkLock/peekLock/runTerraform -- and by extension
// TF_CLI_ARGS_plan injection, backend discovery, and the lock-check warning
// and strict refusal -- wired together end to end through a real
// "parraform plan" invocation.
//
// The state lock is planted directly as an S3 object (mirroring exactly
// what a genuinely in-flight "apply" would have written) rather than by
// racing a real "parraform apply" in the background. That makes both
// scenarios below fully deterministic: no sleep/timing window has to be
// guessed to land "plan" while "apply" happens to be mid-flight.
//
// Gated behind the "integration" build tag, so `go test ./...` never needs
// docker or a terraform binary. Run explicitly with:
//
//	go test -tags=integration ./cmd/parraform/ -run TestE2E -v
//
// Locally, a missing docker/terraform or a failed container start just
// skips the test; set PARRAFORM_REQUIRE_DOCKER=1 (as CI does) to fail
// instead of skip.

const e2eMinistackImage = "ministackorg/ministack@sha256:d865b1e43b0b1a7e6f246e1a46fb8a0a2fa04e2a94c64740c8ff454748040726"

func e2eSkipOrFail(t *testing.T, format string, args ...any) {
	t.Helper()
	if os.Getenv("PARRAFORM_REQUIRE_DOCKER") != "" {
		t.Fatalf(format, args...)
	}
	t.Skipf(format, args...)
}

// e2eStartMinistack starts a ministack container and returns its base URL.
// See internal/lockcheck/s3_integration_test.go for the sibling
// implementation this mirrors; duplicated rather than shared because the
// two live in different packages and the helper is small.
func e2eStartMinistack(t *testing.T) string {
	t.Helper()

	if _, err := exec.LookPath("docker"); err != nil {
		e2eSkipOrFail(t, "docker not available, skipping E2E lock test")
	}

	name := fmt.Sprintf("parraform-e2e-ministack-%d", time.Now().UnixNano())
	runCmd := exec.Command("docker", "run", "-d", "--rm", "--name", name, "-P", e2eMinistackImage)
	var stderr bytes.Buffer
	runCmd.Stderr = &stderr
	if err := runCmd.Run(); err != nil {
		e2eSkipOrFail(t, "failed to start ministack container (docker run: %v): %s", err, stderr.String())
	}
	t.Cleanup(func() {
		_ = exec.Command("docker", "rm", "-f", name).Run()
	})

	out, err := exec.Command("docker", "port", name, "4566/tcp").Output()
	if err != nil {
		t.Fatalf("docker port: %v", err)
	}
	line := strings.SplitN(strings.TrimSpace(string(out)), "\n", 2)[0]
	_, port, ok := strings.Cut(line, ":")
	if !ok || port == "" {
		t.Fatalf("unexpected `docker port` output: %q", out)
	}
	port = strings.TrimPrefix(port, "[")

	host := "127.0.0.1"
	if ctxOut, err := exec.Command("docker", "context", "inspect", "--format", "{{.Endpoints.docker.Host}}").Output(); err == nil {
		if u, err := url.Parse(strings.TrimSpace(string(ctxOut))); err == nil && u.Hostname() != "" {
			switch u.Scheme {
			case "ssh", "tcp", "http", "https":
				host = u.Hostname()
			}
		}
	}

	endpoint := fmt.Sprintf("http://%s:%s", host, port)
	deadline := time.Now().Add(30 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		resp, err := http.Get(endpoint + "/")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return endpoint
			}
			lastErr = fmt.Errorf("unexpected status %d", resp.StatusCode)
		} else {
			lastErr = err
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("ministack did not become ready at %s: %v", endpoint, lastErr)
	return ""
}

// buildParraform compiles the parraform binary under test into a temp
// directory and returns its path.
func buildParraform(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "parraform")
	cmd := exec.Command("go", "build", "-o", bin, ".")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("go build parraform: %v: %s", err, stderr.String())
	}
	return bin
}

func TestE2E_PlanAndTheStateLock(t *testing.T) {
	terraformBin, err := exec.LookPath("terraform")
	if err != nil {
		e2eSkipOrFail(t, "terraform not available, skipping E2E lock test")
	}

	endpoint := e2eStartMinistack(t)
	t.Setenv("AWS_ENDPOINT_URL", endpoint)
	t.Setenv("AWS_ACCESS_KEY_ID", "test")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test")
	t.Setenv("AWS_REGION", "us-east-1")
	t.Setenv("PARRAFORM_TERRAFORM_BIN", terraformBin)

	ctx := context.Background()
	awsCfg, err := config.LoadDefaultConfig(ctx, config.WithRegion("us-east-1"))
	if err != nil {
		t.Fatalf("LoadDefaultConfig() error = %v", err)
	}
	s3Client := s3.NewFromConfig(awsCfg)

	const bucket = "parraform-e2e-bucket"
	if _, err := s3Client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)}); err != nil {
		t.Fatalf("CreateBucket() error = %v", err)
	}

	// A backend-only config with zero resources: terraform's S3 backend
	// already resolves AWS_ENDPOINT_URL the same way loadAWSConfig does, so
	// no "endpoints" block is needed, and with no resources there's no
	// provider to download -- `terraform init` needs no network access
	// beyond ministack itself.
	workDir := t.TempDir()
	mainTF := fmt.Sprintf(`terraform {
  backend "s3" {
    bucket = %q
    key    = "terraform.tfstate"
    region = "us-east-1"

    skip_credentials_validation = true
    skip_requesting_account_id  = true
    skip_metadata_api_check     = true
    skip_region_validation      = true
    use_path_style              = true
    use_lockfile                = true
  }
}
`, bucket)
	if err := os.WriteFile(filepath.Join(workDir, "main.tf"), []byte(mainTF), 0o644); err != nil {
		t.Fatal(err)
	}

	runTF := func(t *testing.T, args ...string) (string, error) {
		t.Helper()
		cmd := exec.CommandContext(ctx, terraformBin, args...)
		cmd.Dir = workDir
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	runParraform := func(t *testing.T, bin string, args ...string) (string, time.Duration, error) {
		t.Helper()
		cmd := exec.CommandContext(ctx, bin, args...)
		cmd.Dir = workDir
		start := time.Now()
		out, err := cmd.CombinedOutput()
		return string(out), time.Since(start), err
	}

	if out, err := runTF(t, "init", "-input=false"); err != nil {
		t.Fatalf("terraform init failed: %v\n%s", err, out)
	}

	parraformBin := buildParraform(t)

	t.Run("not locked: plan succeeds with no lock warning", func(t *testing.T) {
		out, elapsed, err := runParraform(t, parraformBin, "plan", "-lock-timeout=0s")
		if err != nil {
			t.Fatalf("parraform plan failed: %v\n%s", err, out)
		}
		if strings.Contains(out, "state lock is currently held") {
			t.Errorf("unexpected lock warning in output:\n%s", out)
		}
		if !strings.Contains(out, "No changes.") {
			t.Errorf("expected a successful plan, got:\n%s", out)
		}
		t.Logf("plan completed in %v", elapsed)
	})

	lockKey := "terraform.tfstate.tflock"
	plantLock := func(t *testing.T) {
		t.Helper()
		_, err := s3Client.PutObject(ctx, &s3.PutObjectInput{
			Bucket: aws.String(bucket),
			Key:    aws.String(lockKey),
			Body: strings.NewReader(`{
				"ID": "11111111-2222-3333-4444-555555555555",
				"Operation": "OperationTypeApply",
				"Who": "e2e-test@ci",
				"Version": "1.15.8",
				"Created": "2026-01-01T00:00:00Z"
			}`),
		})
		if err != nil {
			t.Fatalf("planting lock object: %v", err)
		}
	}
	clearLock := func(t *testing.T) {
		t.Helper()
		_, err := s3Client.DeleteObject(ctx, &s3.DeleteObjectInput{
			Bucket: aws.String(bucket),
			Key:    aws.String(lockKey),
		})
		if err != nil {
			t.Fatalf("clearing lock object: %v", err)
		}
	}

	t.Run("real terraform plan is blocked by the planted lock (sanity check)", func(t *testing.T) {
		plantLock(t)
		defer clearLock(t)

		out, err := runTF(t, "plan", "-lock-timeout=0s")
		if err == nil {
			t.Fatalf("expected `terraform plan` to fail against a locked state, it succeeded:\n%s", out)
		}
		if !strings.Contains(out, "Error acquiring the state lock") {
			t.Errorf("expected a state-lock error, got:\n%s", out)
		}
	})

	t.Run("locked: parraform apply is still blocked by the lock", func(t *testing.T) {
		// The other subtests all assert the lock-free direction (plan
		// ignores a held lock). This is the other half of the contract:
		// apply must keep locking exactly like real terraform, so a
		// concurrent apply can't corrupt state. If runTerraform's
		// plan-only gate on injecting -lock=false ever broadened to cover
		// apply too, this is the only test in the repo that would catch
		// it -- internal/tfargs only unit-tests PlanEnv in isolation, not
		// the subcommand gate that decides when it's applied.
		plantLock(t)
		defer clearLock(t)

		out, _, err := runParraform(t, parraformBin, "apply", "-auto-approve", "-lock-timeout=0s")
		if err == nil {
			t.Fatalf("expected `parraform apply` to fail against a locked state, it succeeded:\n%s", out)
		}
		if !strings.Contains(out, "Error acquiring the state lock") {
			t.Errorf("expected a state-lock error, got:\n%s", out)
		}
	})

	t.Run("locked: parraform plan still succeeds promptly and warns", func(t *testing.T) {
		plantLock(t)
		defer clearLock(t)

		out, elapsed, err := runParraform(t, parraformBin, "plan", "-lock-timeout=0s")
		if err != nil {
			t.Fatalf("parraform plan failed against a locked state: %v\n%s", err, out)
		}
		if !strings.Contains(out, "state lock is currently held (holder: e2e-test@ci)") {
			t.Errorf("expected the lock-held warning naming the holder, got:\n%s", out)
		}
		if !strings.Contains(out, "running plan unlocked") {
			t.Errorf("expected the warning to say plan runs unlocked (no explicit -lock=true here), got:\n%s", out)
		}
		if !strings.Contains(out, "No changes.") {
			t.Errorf("expected the plan to still complete, got:\n%s", out)
		}
		const bound = 10 * time.Second
		if elapsed > bound {
			t.Errorf("plan took %v against a locked state, want well under %v (it must not block waiting on the lock)", elapsed, bound)
		}
		t.Logf("plan completed in %v despite the held lock", elapsed)
	})

	t.Run("locked, explicit -lock=true: parraform's warning describes the real outcome, then terraform itself fails acquiring the lock", func(t *testing.T) {
		plantLock(t)
		defer clearLock(t)

		out, _, err := runParraform(t, parraformBin, "plan", "-lock-timeout=0s", "-lock=true")
		if err == nil {
			t.Fatalf("expected `parraform plan -lock=true` to fail against a locked state (terraform's own lock acquisition), it succeeded:\n%s", out)
		}
		if !strings.Contains(out, "-lock=true") {
			t.Errorf("expected parraform's warning to mention the explicit -lock=true, got:\n%s", out)
		}
		if strings.Contains(out, "running plan unlocked") {
			t.Errorf("parraform's warning must not claim plan runs unlocked when -lock=true is explicit, got:\n%s", out)
		}
		if !strings.Contains(out, "Error acquiring the state lock") {
			t.Errorf("expected terraform's own lock-acquisition error following parraform's warning, got:\n%s", out)
		}
	})

	t.Run("unlocked: parraform plan with -lock-check=strict still succeeds (flag is stripped, never reaches terraform)", func(t *testing.T) {
		out, _, err := runParraform(t, parraformBin, "plan", "-lock-timeout=0s", "-lock-check=strict")
		if err != nil {
			t.Fatalf("parraform plan failed: %v\n%s", err, out)
		}
		if !strings.Contains(out, "No changes.") {
			t.Errorf("expected a successful plan, got:\n%s", out)
		}
	})

	t.Run("locked: parraform plan with -lock-check=strict refuses without invoking terraform", func(t *testing.T) {
		plantLock(t)
		defer clearLock(t)

		out, elapsed, err := runParraform(t, parraformBin, "plan", "-lock-timeout=0s", "-lock-check=strict")
		if err == nil {
			t.Fatalf("expected parraform plan to fail under -lock-check=strict against a locked state, it succeeded:\n%s", out)
		}
		if !strings.Contains(out, "state lock is currently held (holder: e2e-test@ci)") {
			t.Errorf("expected the refusal to name the holder, got:\n%s", out)
		}
		if strings.Contains(out, "No changes.") {
			t.Errorf("plan must not have run at all under -lock-check=strict, got:\n%s", out)
		}
		const bound = 10 * time.Second
		if elapsed > bound {
			t.Errorf("refusal took %v, want well under %v (it must fail fast, before even attempting terraform)", elapsed, bound)
		}
		t.Logf("plan refused in %v", elapsed)
	})

	t.Run("unlocked: -lock-check=strict via TF_CLI_ARGS_plan also succeeds (also stripped, never reaches terraform)", func(t *testing.T) {
		t.Setenv("TF_CLI_ARGS_plan", "-lock-check=strict")

		out, _, err := runParraform(t, parraformBin, "plan", "-lock-timeout=0s")
		if err != nil {
			t.Fatalf("parraform plan failed: %v\n%s", err, out)
		}
		if !strings.Contains(out, "No changes.") {
			t.Errorf("expected a successful plan, got:\n%s", out)
		}
	})

	t.Run("locked: -lock-check=strict via TF_CLI_ARGS_plan refuses too, for callers that can't pass CLI flags (e.g. Atlantis/terragrunt)", func(t *testing.T) {
		plantLock(t)
		defer clearLock(t)
		t.Setenv("TF_CLI_ARGS_plan", "-lock-check=strict")

		out, _, err := runParraform(t, parraformBin, "plan", "-lock-timeout=0s")
		if err == nil {
			t.Fatalf("expected parraform plan to fail under TF_CLI_ARGS_plan=-lock-check=strict against a locked state, it succeeded:\n%s", out)
		}
		if !strings.Contains(out, "state lock is currently held (holder: e2e-test@ci)") {
			t.Errorf("expected the refusal to name the holder, got:\n%s", out)
		}
		if strings.Contains(out, "No changes.") {
			t.Errorf("plan must not have run at all, got:\n%s", out)
		}
	})

	t.Run("an explicit CLI -lock-check=warn overrides TF_CLI_ARGS_plan's strict", func(t *testing.T) {
		plantLock(t)
		defer clearLock(t)
		t.Setenv("TF_CLI_ARGS_plan", "-lock-check=strict")

		out, _, err := runParraform(t, parraformBin, "plan", "-lock-timeout=0s", "-lock-check=warn")
		if err != nil {
			t.Fatalf("parraform plan failed: %v\n%s", err, out)
		}
		if !strings.Contains(out, "No changes.") {
			t.Errorf("expected the CLI flag to win over TF_CLI_ARGS_plan and let plan complete, got:\n%s", out)
		}
	})

	t.Run("invalid -lock-check value is a parraform-level usage error, not a terraform one", func(t *testing.T) {
		out, _, err := runParraform(t, parraformBin, "plan", "-lock-timeout=0s", "-lock-check=bogus")
		if err == nil {
			t.Fatalf("expected parraform plan to fail on an invalid -lock-check value, it succeeded:\n%s", out)
		}
		if !strings.Contains(out, `invalid -lock-check value "bogus"`) {
			t.Errorf("expected a parraform usage error naming the bad value, got:\n%s", out)
		}
		if strings.Contains(out, "No changes.") {
			t.Errorf("plan must not have run at all on an invalid -lock-check value, got:\n%s", out)
		}
	})

	t.Run("locked: N concurrent parraform plans all succeed without contending", func(t *testing.T) {
		plantLock(t)
		defer clearLock(t)

		const n = 10
		var wg sync.WaitGroup
		results := make([]struct {
			out string
			err error
		}, n)
		for i := 0; i < n; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				out, _, err := runParraform(t, parraformBin, "plan", "-lock-timeout=0s")
				results[i].out, results[i].err = out, err
			}(i)
		}
		wg.Wait()

		for i, r := range results {
			if r.err != nil {
				t.Errorf("run %d: parraform plan failed: %v\n%s", i, r.err, r.out)
				continue
			}
			if !strings.Contains(r.out, "No changes.") {
				t.Errorf("run %d: expected a successful plan, got:\n%s", i, r.out)
			}
		}
	})
}
