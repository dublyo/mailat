package provider

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// ErrAttachmentNotFound means the object is gone; retrying cannot help.
var ErrAttachmentNotFound = errors.New("attachment object not found")

type AttachmentStorage interface {
	Put(context.Context, string, string, []byte) error
	Get(context.Context, string, string) ([]byte, error)
}

type s3AttachmentStorage struct{ client *s3.Client }

func NewAttachmentStorage(ctx context.Context, region, accessKey, secret string) (AttachmentStorage, error) {
	cfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(region), awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(accessKey, secret, "")))
	if err != nil {
		return nil, err
	}
	return &s3AttachmentStorage{client: s3.NewFromConfig(cfg)}, nil
}

func (s *s3AttachmentStorage) Put(ctx context.Context, bucket, key string, data []byte) error {
	_, err := s.client.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(bucket), Key: aws.String(key), Body: bytes.NewReader(data), ContentType: aws.String("application/octet-stream")})
	return err
}
func (s *s3AttachmentStorage) Get(ctx context.Context, bucket, key string) ([]byte, error) {
	result, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
	var missing *s3types.NoSuchKey
	if errors.As(err, &missing) {
		return nil, ErrAttachmentNotFound
	}
	if err != nil {
		return nil, err
	}
	defer result.Body.Close()
	data, err := io.ReadAll(io.LimitReader(result.Body, MaxAttachmentBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > MaxAttachmentBytes {
		return nil, fmt.Errorf("attachment exceeds 10 MiB")
	}
	return data, nil
}
