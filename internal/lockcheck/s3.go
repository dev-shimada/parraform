package lockcheck

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"

	"github.com/dev-shimada/parraform/internal/backendcfg"
)

func init() {
	Register("s3", s3Checker{})
}

// lockInfoJSON mirrors the fields terraform writes into its lock payload
// (both the S3 native .tflock object body and the DynamoDB "Info"
// attribute use this same JSON shape).
type lockInfoJSON struct {
	Who string `json:"Who"`
}

// s3Checker peeks the S3 backend's lock. It supports both locking
// mechanisms: the newer native S3 lockfile (use_lockfile = true, TF >=
// 1.11) and the legacy DynamoDB-table mechanism (dynamodb_table). If
// neither is configured, the backend has no locking at all and Peek
// reports unsupported.
type s3Checker struct{}

func (s3Checker) Peek(ctx context.Context, cfg backendcfg.Config) (Info, bool, error) {
	bucket, _ := cfg.Config["bucket"].(string)
	key, _ := cfg.Config["key"].(string)
	if bucket == "" || key == "" {
		return Info{}, false, nil
	}

	useLockfile, _ := cfg.Config["use_lockfile"].(bool)
	table, _ := cfg.Config["dynamodb_table"].(string)
	if !useLockfile && table == "" {
		// No locking mechanism configured for this backend: nothing to
		// peek, and terraform itself won't be locking either.
		return Info{}, false, nil
	}

	awsCfg, err := loadAWSConfig(ctx, cfg.Config)
	if err != nil {
		return Info{}, false, fmt.Errorf("loading AWS config: %w", err)
	}

	if useLockfile {
		client := s3.NewFromConfig(awsCfg)
		return peekS3Lockfile(ctx, client, bucket, key)
	}

	client := dynamodb.NewFromConfig(awsCfg)
	return peekDynamoDBLock(ctx, client, table, bucket, key)
}

func loadAWSConfig(ctx context.Context, bcfg map[string]any) (aws.Config, error) {
	var opts []func(*config.LoadOptions) error
	if region, _ := bcfg["region"].(string); region != "" {
		opts = append(opts, config.WithRegion(region))
	}
	if profile, _ := bcfg["profile"].(string); profile != "" {
		opts = append(opts, config.WithSharedConfigProfile(profile))
	}
	return config.LoadDefaultConfig(ctx, opts...)
}

// s3GetObjectAPI is the slice of *s3.Client used here, so tests can point
// it at a fake server without touching real AWS credentials or endpoints.
type s3GetObjectAPI interface {
	GetObject(ctx context.Context, params *s3.GetObjectInput, optFns ...func(*s3.Options)) (*s3.GetObjectOutput, error)
}

func peekS3Lockfile(ctx context.Context, client s3GetObjectAPI, bucket, key string) (Info, bool, error) {
	lockKey := key + ".tflock"

	out, err := client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(lockKey),
	})
	if err != nil {
		if isNotFound(err) {
			return Info{Locked: false}, true, nil
		}
		return Info{}, false, err
	}
	defer out.Body.Close()

	body, err := io.ReadAll(out.Body)
	if err != nil {
		// The lock object exists; treat as locked even if we can't read
		// the body describing who holds it.
		return Info{Locked: true}, true, nil
	}

	var li lockInfoJSON
	_ = json.Unmarshal(body, &li)
	return Info{Locked: true, Who: li.Who}, true, nil
}

func isNotFound(err error) bool {
	var apiErr smithy.APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	switch apiErr.ErrorCode() {
	case "NoSuchKey", "NotFound":
		return true
	default:
		return false
	}
}

type dynamoDBGetItemAPI interface {
	GetItem(ctx context.Context, params *dynamodb.GetItemInput, optFns ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error)
}

func peekDynamoDBLock(ctx context.Context, client dynamoDBGetItemAPI, table, bucket, key string) (Info, bool, error) {
	lockID := bucket + "/" + key

	out, err := client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(table),
		Key: map[string]ddbtypes.AttributeValue{
			"LockID": &ddbtypes.AttributeValueMemberS{Value: lockID},
		},
	})
	if err != nil {
		return Info{}, false, err
	}
	if len(out.Item) == 0 {
		return Info{Locked: false}, true, nil
	}

	who := ""
	if av, ok := out.Item["Info"]; ok {
		if s, ok := av.(*ddbtypes.AttributeValueMemberS); ok {
			var li lockInfoJSON
			if json.Unmarshal([]byte(s.Value), &li) == nil {
				who = li.Who
			}
		}
	}
	return Info{Locked: true, Who: who}, true, nil
}
