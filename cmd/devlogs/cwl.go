package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs/types"
)

// runCWL は MiniStack の CloudWatch Logs の /sp/local/* に Logs Insights のクエリを流して結果を表示する。
// MiniStack で使えるのは fields・filter・sort・limit だけ（集計は本番でしか試せない）。
func runCWL(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("cwl", flag.ContinueOnError)
	since := fs.Duration("since", time.Hour, "対象の期間")
	prefix := fs.String("groups", "/sp/local/", "ロググループの接頭辞")
	var query string
	var rest []string
	for _, a := range args {
		if !strings.HasPrefix(a, "-") && query == "" {
			query = a
			continue
		}
		rest = append(rest, a)
	}
	if err := fs.Parse(rest); err != nil {
		return err
	}
	if query == "" {
		return errors.New("query is required")
	}
	// 必ず MiniStack に向ける（AWS_ENDPOINT_URL がなくても本物の AWS には接続しない）
	endpoint := os.Getenv("AWS_ENDPOINT_URL")
	if endpoint == "" {
		endpoint = "http://localhost:4566"
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion("ap-northeast-1"),
		awsconfig.WithBaseEndpoint(endpoint),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider("test", "test", "")))
	if err != nil {
		return err
	}
	c := cloudwatchlogs.NewFromConfig(cfg)
	groups, err := c.DescribeLogGroups(ctx, &cloudwatchlogs.DescribeLogGroupsInput{LogGroupNamePrefix: aws.String(*prefix)})
	if err != nil {
		return fmt.Errorf("describe log groups: %w", err)
	}
	var names []string
	for _, g := range groups.LogGroups {
		names = append(names, aws.ToString(g.LogGroupName))
	}
	if len(names) == 0 {
		return fmt.Errorf("no log groups under %s", *prefix)
	}
	now := time.Now()
	q, err := c.StartQuery(ctx, &cloudwatchlogs.StartQueryInput{
		LogGroupNames: names, QueryString: aws.String(query),
		StartTime: aws.Int64(now.Add(-*since).Unix()), EndTime: aws.Int64(now.Add(time.Minute).Unix()),
	})
	if err != nil {
		return fmt.Errorf("start query: %w", err)
	}
	for range 60 {
		res, err := c.GetQueryResults(ctx, &cloudwatchlogs.GetQueryResultsInput{QueryId: q.QueryId})
		if err != nil {
			return fmt.Errorf("get query results: %w", err)
		}
		switch res.Status {
		case types.QueryStatusComplete:
			fmt.Fprintf(out, "# %d rows from %s\n", len(res.Results), strings.Join(names, ","))
			for _, row := range res.Results {
				var cols []string
				for _, f := range row {
					if aws.ToString(f.Field) == "@ptr" {
						continue
					}
					cols = append(cols, aws.ToString(f.Field)+"="+aws.ToString(f.Value))
				}
				fmt.Fprintln(out, strings.Join(cols, " "))
			}
			return nil
		case types.QueryStatusFailed, types.QueryStatusCancelled, types.QueryStatusTimeout:
			return fmt.Errorf("query %s", res.Status)
		default: // Scheduled・Running は待つ
		}
		time.Sleep(500 * time.Millisecond)
	}
	return errors.New("query did not finish in 30s")
}
