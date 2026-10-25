package messaging

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"

	"subscription-platform/internal/platform/observability"
)

// ConsumerConfig は受信ループの設定。
type ConsumerConfig struct {
	Client            *sqs.Client
	QueueURL, DLQURL  string
	Concurrency       int
	VisibilityTimeout time.Duration
	WaitTime          time.Duration // ロングポーリング（既定 20 秒）
	ShutdownGrace     time.Duration // 既定 30 秒
	RetryBase         time.Duration // 一時失敗の可視性 = min(RetryBase×2^(受信回数−1), RetryMax)。既定 5 秒
	RetryMax          time.Duration // 既定 15 分
}

func (c *ConsumerConfig) defaults(q QueueSpec) {
	if c.Concurrency <= 0 {
		c.Concurrency = q.Concurrency
	}
	if c.VisibilityTimeout <= 0 {
		c.VisibilityTimeout = time.Duration(q.VisibilityTimeoutS) * time.Second
	}
	if c.WaitTime <= 0 {
		c.WaitTime = 20 * time.Second
	}
	if c.ShutdownGrace <= 0 {
		c.ShutdownGrace = 30 * time.Second
	}
	if c.RetryBase <= 0 {
		c.RetryBase = 5 * time.Second
	}
	if c.RetryMax <= 0 {
		c.RetryMax = 15 * time.Minute
	}
}

// RunConsumer は SQS の常駐の受信ループ（sqsconsumer）。ctx が取り消されたら受信を止め、処理中を ShutdownGrace まで待つ。
func RunConsumer(ctx context.Context, p Processor, cfg ConsumerConfig) error {
	q := p.Queue()
	cfg.defaults(q)
	c := &consumer{p: p, q: q, cfg: cfg, sem: make(chan struct{}, cfg.Concurrency), log: observability.Logger("platform.consumer")}
	return c.run(ctx)
}

type consumer struct {
	p   Processor
	q   QueueSpec
	cfg ConsumerConfig
	sem chan struct{}
	wg  sync.WaitGroup
	log *slog.Logger
}

func (c *consumer) run(ctx context.Context) error {
	// 処理中のメッセージは受信ループの ctx と切り離す（SIGTERM で途中で止めない）
	work := context.WithoutCancel(ctx)
	for ctx.Err() == nil {
		// 空きができるまで待つ
		select {
		case c.sem <- struct{}{}:
		case <-ctx.Done():
			continue
		}
		free := 1
		for free < 10 && len(c.sem) < cap(c.sem) {
			c.sem <- struct{}{}
			free++
		}
		msgs, err := c.receive(ctx, free)
		if len(msgs) < free {
			for range free - len(msgs) {
				<-c.sem
			}
		}
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			c.log.LogAttrs(ctx, slog.LevelWarn, "consumer.receive_failed", slog.String("queue", c.q.Name), observability.Err(err))
			sleep(ctx, time.Second)
			continue
		}
		if c.q.FIFO {
			c.wg.Add(1)
			go func() {
				defer c.wg.Done()
				defer func() {
					for range msgs {
						<-c.sem
					}
				}()
				c.process(work, msgs)
			}()
			continue
		}
		for _, m := range msgs {
			c.wg.Add(1)
			go func() {
				defer c.wg.Done()
				defer func() { <-c.sem }()
				c.process(work, []Message{m})
			}()
		}
	}
	done := make(chan struct{})
	go func() { c.wg.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-time.After(c.cfg.ShutdownGrace):
		c.log.LogAttrs(ctx, slog.LevelWarn, "consumer.shutdown_timeout", slog.String("queue", c.q.Name))
		return errors.New("messaging: shutdown grace exceeded")
	}
}

func sleep(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
	case <-ctx.Done():
	}
}

func (c *consumer) receive(ctx context.Context, n int) ([]Message, error) {
	in := &sqs.ReceiveMessageInput{
		QueueUrl:                    aws.String(c.cfg.QueueURL),
		MaxNumberOfMessages:         int32(n), //nolint:gosec // n <= 10
		WaitTimeSeconds:             int32(c.cfg.WaitTime / time.Second),
		VisibilityTimeout:           int32(c.cfg.VisibilityTimeout / time.Second),
		MessageSystemAttributeNames: []sqstypes.MessageSystemAttributeName{sqstypes.MessageSystemAttributeNameApproximateReceiveCount, sqstypes.MessageSystemAttributeNameMessageGroupId},
	}
	out, err := c.cfg.Client.ReceiveMessage(ctx, in)
	if err != nil {
		return nil, err //nolint:wrapcheck
	}
	msgs := make([]Message, 0, len(out.Messages))
	for _, m := range out.Messages {
		rc, _ := strconv.Atoi(m.Attributes[string(sqstypes.MessageSystemAttributeNameApproximateReceiveCount)])
		msgs = append(msgs, Message{
			ID: aws.ToString(m.MessageId), Body: aws.ToString(m.Body), ReceiptHandle: aws.ToString(m.ReceiptHandle),
			GroupID: m.Attributes[string(sqstypes.MessageSystemAttributeNameMessageGroupId)], ReceiveCount: rc,
		})
	}
	return msgs, nil
}

func (c *consumer) process(ctx context.Context, msgs []Message) {
	stop := c.extendVisibility(ctx, msgs)
	out := c.p.Dispatch(ctx, msgs)
	stop()
	for _, m := range out.Succeeded {
		c.delete(ctx, m)
	}
	for _, m := range out.Retry {
		backoff := c.cfg.RetryBase << max(m.ReceiveCount-1, 0)
		if backoff > c.cfg.RetryMax || backoff <= 0 {
			backoff = c.cfg.RetryMax
		}
		if d, ok := out.Delay[m.ID]; ok {
			backoff = d
		}
		c.changeVisibility(ctx, m, backoff)
		if m.ReceiveCount >= c.q.MaxReceive {
			c.log.LogAttrs(ctx, slog.LevelError, "event.dlq", slog.String("queue", c.q.Name), slog.String("message_id", m.ID),
				slog.Int("receive_count", m.ReceiveCount), slog.String("reason", "max_receive_exceeded"))
		}
	}
	for _, m := range out.Permanent {
		c.toDLQ(ctx, m)
	}
}

// extendVisibility は処理中、VT/2 ごとに可視性を VT だけ延ばす（E9: 延長に失敗しても処理は続ける）。
func (c *consumer) extendVisibility(ctx context.Context, msgs []Message) func() {
	done := make(chan struct{})
	go func() {
		t := time.NewTicker(c.cfg.VisibilityTimeout / 2)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				for _, m := range msgs {
					c.changeVisibility(ctx, m, c.cfg.VisibilityTimeout)
				}
			}
		}
	}()
	var once sync.Once
	return func() { once.Do(func() { close(done) }) }
}

func (c *consumer) changeVisibility(ctx context.Context, m Message, d time.Duration) {
	_, err := c.cfg.Client.ChangeMessageVisibility(ctx, &sqs.ChangeMessageVisibilityInput{
		QueueUrl: aws.String(c.cfg.QueueURL), ReceiptHandle: aws.String(m.ReceiptHandle), VisibilityTimeout: int32(d / time.Second),
	})
	if err != nil {
		c.log.LogAttrs(ctx, slog.LevelWarn, "consumer.change_visibility_failed", slog.String("queue", c.q.Name), slog.String("message_id", m.ID), observability.Err(err))
	}
}

func (c *consumer) delete(ctx context.Context, m Message) {
	if _, err := c.cfg.Client.DeleteMessage(ctx, &sqs.DeleteMessageInput{QueueUrl: aws.String(c.cfg.QueueURL), ReceiptHandle: aws.String(m.ReceiptHandle)}); err != nil {
		c.log.LogAttrs(ctx, slog.LevelWarn, "consumer.delete_failed", slog.String("queue", c.q.Name), slog.String("message_id", m.ID), observability.Err(err))
	}
}

// toDLQ は Permanent のメッセージを DLQ に送って元を削除する（E7）。
func (c *consumer) toDLQ(ctx context.Context, m Message) {
	in := &sqs.SendMessageInput{QueueUrl: aws.String(c.cfg.DLQURL), MessageBody: aws.String(m.Body)}
	if c.q.FIFO {
		in.MessageGroupId = aws.String(m.GroupID)
		in.MessageDeduplicationId = aws.String(m.ID)
	}
	if _, err := c.cfg.Client.SendMessage(ctx, in); err != nil {
		c.log.LogAttrs(ctx, slog.LevelError, "consumer.dlq_send_failed", slog.String("queue", c.q.Name), slog.String("message_id", m.ID), observability.Err(err))
		return
	}
	c.log.LogAttrs(ctx, slog.LevelError, "event.dlq", slog.String("queue", c.q.Name), slog.String("message_id", m.ID),
		slog.Int("receive_count", m.ReceiveCount), slog.String("reason", "permanent"))
	c.delete(ctx, m)
}
