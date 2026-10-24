// Package awsx は AWS SDK の設定を作る。AWS_ENDPOINT_URL（MiniStack）は SDK 標準の変数に任せ、コードで分岐しない。
package awsx

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
)

// Load は既定の設定（環境変数・共有設定・ロール）を読む。
func Load(ctx context.Context, region string) (aws.Config, error) {
	cfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(region))
	if err != nil {
		return aws.Config{}, fmt.Errorf("awsx: load config: %w", err)
	}
	return cfg, nil
}

// Static はテスト用に、エンドポイントとダミーの認証情報を明示した設定を作る。
func Static(region, endpoint string) aws.Config {
	return aws.Config{
		Region:       region,
		Credentials:  credentials.NewStaticCredentialsProvider("test", "test", ""),
		BaseEndpoint: aws.String(endpoint),
	}
}
