//go:build integration

// Package awstest は結合テスト用に MiniStack を起動し、キュー・バス・ルールを作る補助を持つ。
// AWS_ENDPOINT_URL が設定されていれば、それ（CI で起動済みの MiniStack）を使う。
package awstest

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge"
	ebtypes "github.com/aws/aws-sdk-go-v2/service/eventbridge/types"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"subscription-platform/internal/platform/awsx"
)

// Image は compose.yaml と同じ MiniStack のイメージ。
const Image = "ministackorg/ministack:1.5.32"

const Region = "ap-northeast-1"

var (
	once     sync.Once
	endpoint string
	startErr error
	ctr      testcontainers.Container
)

// Endpoint は MiniStack の URL を返す（最初の呼び出しで起動する）。
func Endpoint(t testing.TB) string {
	t.Helper()
	once.Do(func() {
		if e := os.Getenv("AWS_ENDPOINT_URL"); e != "" {
			endpoint = e
			return
		}
		ctx := context.Background()
		ctr, startErr = testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
			ContainerRequest: testcontainers.ContainerRequest{
				Image:        Image,
				ExposedPorts: []string{"4566/tcp"},
				WaitingFor:   wait.ForHTTP("/_ministack/health").WithPort("4566/tcp").WithStartupTimeout(90 * time.Second),
			},
			Started: true,
		})
		if startErr != nil {
			return
		}
		host, _ := ctr.Host(ctx)
		port, _ := ctr.MappedPort(ctx, "4566/tcp")
		endpoint = fmt.Sprintf("http://%s:%s", host, port.Port())
	})
	if startErr != nil {
		t.Fatalf("awstest: start ministack: %v", startErr)
	}
	return endpoint
}

// Terminate は TestMain の最後に呼ぶ。
func Terminate() {
	if ctr != nil {
		_ = ctr.Terminate(context.Background())
	}
}

// Config は MiniStack 向けの設定。
func Config(t testing.TB) aws.Config { return awsx.Static(Region, Endpoint(t)) }

func SQS(t testing.TB) *sqs.Client { return sqs.NewFromConfig(Config(t)) }

func EventBridge(t testing.TB) *eventbridge.Client { return eventbridge.NewFromConfig(Config(t)) }

// QueueOptions はテスト用のキューの設定。
type QueueOptions struct {
	FIFO              bool
	VisibilityTimeout int
	MaxReceive        int // > 0 なら DLQ を作って redrive を設定する
}

// CreateQueue はキュー（と DLQ）を作り、URL を返す。
func CreateQueue(t testing.TB, name string, o QueueOptions) (url, dlqURL string) {
	t.Helper()
	c := SQS(t)
	ctx := context.Background()
	attrs := map[string]string{}
	dlqName := name + "-dlq"
	if o.FIFO {
		attrs["FifoQueue"] = "true"
		name += ".fifo"
		dlqName += ".fifo"
	}
	if o.MaxReceive > 0 {
		d, err := c.CreateQueue(ctx, &sqs.CreateQueueInput{QueueName: aws.String(dlqName), Attributes: attrs})
		if err != nil {
			t.Fatal(err)
		}
		dlqURL = aws.ToString(d.QueueUrl)
		arn := QueueARN(t, dlqURL)
		rp, _ := json.Marshal(map[string]string{"deadLetterTargetArn": arn, "maxReceiveCount": strconv.Itoa(o.MaxReceive)})
		attrs = clone(attrs)
		attrs["RedrivePolicy"] = string(rp)
	}
	if o.VisibilityTimeout > 0 {
		attrs["VisibilityTimeout"] = strconv.Itoa(o.VisibilityTimeout)
	}
	q, err := c.CreateQueue(ctx, &sqs.CreateQueueInput{QueueName: aws.String(name), Attributes: attrs})
	if err != nil {
		t.Fatal(err)
	}
	return aws.ToString(q.QueueUrl), dlqURL
}

func clone(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// QueueARN はキューの ARN。
func QueueARN(t testing.TB, url string) string {
	t.Helper()
	out, err := SQS(t).GetQueueAttributes(context.Background(), &sqs.GetQueueAttributesInput{QueueUrl: aws.String(url), AttributeNames: []sqstypes.QueueAttributeName{sqstypes.QueueAttributeNameQueueArn}})
	if err != nil {
		t.Fatal(err)
	}
	return out.Attributes[string(sqstypes.QueueAttributeNameQueueArn)]
}

// CreateBusWithRule はバスと、pattern でキューに送るルールを作る。
func CreateBusWithRule(t testing.TB, bus, rule, pattern, queueURL string) {
	t.Helper()
	c := EventBridge(t)
	ctx := context.Background()
	if _, err := c.CreateEventBus(ctx, &eventbridge.CreateEventBusInput{Name: aws.String(bus)}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.PutRule(ctx, &eventbridge.PutRuleInput{Name: aws.String(rule), EventBusName: aws.String(bus), EventPattern: aws.String(pattern)}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.PutTargets(ctx, &eventbridge.PutTargetsInput{Rule: aws.String(rule), EventBusName: aws.String(bus), Targets: []ebtypes.Target{{Id: aws.String("q"), Arn: aws.String(QueueARN(t, queueURL))}}}); err != nil {
		t.Fatal(err)
	}
}

// DeleteBus はルールとバスを消す（障害の再現用）。
func DeleteBus(t testing.TB, bus, rule string) {
	t.Helper()
	c := EventBridge(t)
	ctx := context.Background()
	_, _ = c.RemoveTargets(ctx, &eventbridge.RemoveTargetsInput{Rule: aws.String(rule), EventBusName: aws.String(bus), Ids: []string{"q"}})
	_, _ = c.DeleteRule(ctx, &eventbridge.DeleteRuleInput{Name: aws.String(rule), EventBusName: aws.String(bus)})
	if _, err := c.DeleteEventBus(ctx, &eventbridge.DeleteEventBusInput{Name: aws.String(bus)}); err != nil {
		t.Fatal(err)
	}
}

// Drain はキューのメッセージを全部受け取って消し、本文を返す（空の受信が 2 回続くまで）。
func Drain(t testing.TB, url string) []string {
	t.Helper()
	c := SQS(t)
	ctx := context.Background()
	var bodies []string
	empty := 0
	for empty < 2 {
		// 消し損ねたメッセージが受信のあいだに見えるようになって重複に数えられないよう、可視性タイムアウトを長くする
		out, err := c.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{QueueUrl: aws.String(url), MaxNumberOfMessages: 10, WaitTimeSeconds: 1, VisibilityTimeout: 300})
		if err != nil {
			t.Fatal(err)
		}
		if len(out.Messages) == 0 {
			empty++
			continue
		}
		empty = 0
		for _, m := range out.Messages {
			bodies = append(bodies, aws.ToString(m.Body))
			if _, err := c.DeleteMessage(ctx, &sqs.DeleteMessageInput{QueueUrl: aws.String(url), ReceiptHandle: m.ReceiptHandle}); err != nil {
				t.Logf("awstest: メッセージを消せなかった: %v", err)
			}
		}
	}
	return bodies
}

// DrainAtLeast は want 件そろうか timeout が過ぎるまで受信して消す（EventBridge → SQS の配送は非同期で、
// 負荷の高い CI では Drain の「2 回続けて空」より遅れて届くことがある）。want 件を超えて届いた分も返す。
func DrainAtLeast(t testing.TB, url string, want int, timeout time.Duration) []string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var bodies []string
	for len(bodies) < want && time.Now().Before(deadline) {
		bodies = append(bodies, Drain(t, url)...)
	}
	return append(bodies, Drain(t, url)...) // 遅れて届いた重複も数える
}

// ApproxCount はキューの見えているメッセージ数と処理中の数。
func ApproxCount(t testing.TB, url string) (visible, inFlight int) {
	t.Helper()
	out, err := SQS(t).GetQueueAttributes(context.Background(), &sqs.GetQueueAttributesInput{QueueUrl: aws.String(url), AttributeNames: []sqstypes.QueueAttributeName{
		sqstypes.QueueAttributeNameApproximateNumberOfMessages, sqstypes.QueueAttributeNameApproximateNumberOfMessagesNotVisible,
	}})
	if err != nil {
		t.Fatal(err)
	}
	visible, _ = strconv.Atoi(out.Attributes[string(sqstypes.QueueAttributeNameApproximateNumberOfMessages)])
	inFlight, _ = strconv.Atoi(out.Attributes[string(sqstypes.QueueAttributeNameApproximateNumberOfMessagesNotVisible)])
	return visible, inFlight
}
