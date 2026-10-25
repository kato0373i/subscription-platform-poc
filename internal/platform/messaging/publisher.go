package messaging

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge"
	ebtypes "github.com/aws/aws-sdk-go-v2/service/eventbridge/types"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"

	sk "subscription-platform/internal/sharedkernel"
)

// EventBridgePublisher は PutEvents で送る（MESSAGING_MODE=eventbridge）。
type EventBridgePublisher struct {
	client *eventbridge.Client
	bus    string
}

func NewEventBridgePublisher(c *eventbridge.Client, bus string) *EventBridgePublisher {
	return &EventBridgePublisher{client: c, bus: bus}
}

// Publish は 10 件ずつ PutEvents する。一部失敗（FailedEntryCount > 0）は 1 件ごとの Err で返す（E4）。
func (p *EventBridgePublisher) Publish(ctx context.Context, evs []sk.EventEnvelope) ([]PublishResult, error) {
	results := make([]PublishResult, 0, len(evs))
	for start := 0; start < len(evs); start += 10 {
		chunk := evs[start:min(start+10, len(evs))]
		entries := make([]ebtypes.PutEventsRequestEntry, len(chunk))
		for i, e := range chunk {
			detail, err := json.Marshal(e)
			if err != nil {
				return nil, fmt.Errorf("messaging: marshal envelope: %w", err)
			}
			entries[i] = ebtypes.PutEventsRequestEntry{
				EventBusName: aws.String(p.bus),
				Source:       aws.String("sp." + e.Module()),
				DetailType:   aws.String(e.EventType),
				Detail:       aws.String(string(detail)),
				Time:         aws.Time(e.OccurredAt),
			}
		}
		out, err := p.client.PutEvents(ctx, &eventbridge.PutEventsInput{Entries: entries})
		if err != nil {
			for _, e := range chunk {
				results = append(results, PublishResult{EventID: e.EventID, Err: fmt.Errorf("messaging: put events: %w", err)})
			}
			continue
		}
		for i, e := range chunk {
			r := PublishResult{EventID: e.EventID}
			if i < len(out.Entries) && out.Entries[i].ErrorCode != nil {
				r.Err = fmt.Errorf("messaging: put events entry: %s %s", aws.ToString(out.Entries[i].ErrorCode), aws.ToString(out.Entries[i].ErrorMessage))
			}
			results = append(results, r)
		}
	}
	return results, nil
}

// FanoutPublisher は messaging.yaml を読んで、EventBridge 形式の本文を各キューに直接送る（MESSAGING_MODE=direct）。
type FanoutPublisher struct {
	client    *sqs.Client
	topology  Topology
	urlPrefix string
}

func NewFanoutPublisher(c *sqs.Client, t Topology, urlPrefix string) *FanoutPublisher {
	return &FanoutPublisher{client: c, topology: t, urlPrefix: urlPrefix}
}

func (p *FanoutPublisher) Publish(ctx context.Context, evs []sk.EventEnvelope) ([]PublishResult, error) {
	type item struct {
		idx  int
		body string
	}
	byQueue := map[string][]item{}
	results := make([]PublishResult, len(evs))
	for i, e := range evs {
		results[i].EventID = e.EventID
		body, err := EncodeEventBridgeBody(e)
		if err != nil {
			results[i].Err = err
			continue
		}
		for _, q := range p.topology.Subscribers(e.EventType) {
			byQueue[q.Name] = append(byQueue[q.Name], item{idx: i, body: body})
		}
	}
	for queue, items := range byQueue {
		for start := 0; start < len(items); start += 10 {
			chunk := items[start:min(start+10, len(items))]
			entries := make([]sqstypes.SendMessageBatchRequestEntry, len(chunk))
			for i, it := range chunk {
				entries[i] = sqstypes.SendMessageBatchRequestEntry{Id: aws.String(strconv.Itoa(i)), MessageBody: aws.String(it.body)}
			}
			out, err := p.client.SendMessageBatch(ctx, &sqs.SendMessageBatchInput{QueueUrl: aws.String(QueueURL(p.urlPrefix, queue)), Entries: entries})
			if err != nil {
				for _, it := range chunk {
					results[it.idx].Err = fmt.Errorf("messaging: send to %s: %w", queue, err)
				}
				continue
			}
			for _, f := range out.Failed {
				i, _ := strconv.Atoi(aws.ToString(f.Id))
				results[chunk[i].idx].Err = fmt.Errorf("messaging: send to %s: %s", queue, aws.ToString(f.Message))
			}
		}
	}
	return results, nil
}

// SQSCommandSender は FIFO キューに envelope をそのまま本文にして送る（C-01）。
type SQSCommandSender struct {
	client    *sqs.Client
	urlPrefix string
}

func NewSQSCommandSender(c *sqs.Client, urlPrefix string) *SQSCommandSender {
	return &SQSCommandSender{client: c, urlPrefix: urlPrefix}
}

func (s *SQSCommandSender) SendFIFO(ctx context.Context, queue string, env sk.EventEnvelope, groupID, dedupID string) error {
	b, err := json.Marshal(env)
	if err != nil {
		return fmt.Errorf("messaging: marshal envelope: %w", err)
	}
	_, err = s.client.SendMessage(ctx, &sqs.SendMessageInput{
		QueueUrl:               aws.String(QueueURL(s.urlPrefix, queue)),
		MessageBody:            aws.String(string(b)),
		MessageGroupId:         aws.String(groupID),
		MessageDeduplicationId: aws.String(dedupID),
	})
	if err != nil {
		return fmt.Errorf("messaging: send fifo %s: %w", queue, err)
	}
	return nil
}

// FIFOMessage は SendFIFOBatch の 1 件。
type FIFOMessage struct {
	Env              sk.EventEnvelope
	GroupID, DedupID string
}

// SendFIFOBatch は FIFO キューに 10 件ずつ送る（SendMessageBatch。orchestration.md §4.2）。1 件でも失敗したらエラー（呼び出し元はチャンクごと再送する）。
func (s *SQSCommandSender) SendFIFOBatch(ctx context.Context, queue string, msgs []FIFOMessage) error {
	url := aws.String(QueueURL(s.urlPrefix, queue))
	for start := 0; start < len(msgs); start += 10 {
		chunk := msgs[start:min(start+10, len(msgs))]
		entries := make([]sqstypes.SendMessageBatchRequestEntry, len(chunk))
		for i, m := range chunk {
			b, err := json.Marshal(m.Env)
			if err != nil {
				return fmt.Errorf("messaging: marshal envelope: %w", err)
			}
			entries[i] = sqstypes.SendMessageBatchRequestEntry{
				Id: aws.String(fmt.Sprintf("m%d", i)), MessageBody: aws.String(string(b)), MessageGroupId: aws.String(m.GroupID), MessageDeduplicationId: aws.String(m.DedupID),
			}
		}
		out, err := s.client.SendMessageBatch(ctx, &sqs.SendMessageBatchInput{QueueUrl: url, Entries: entries})
		if err != nil {
			return fmt.Errorf("messaging: send fifo batch %s: %w", queue, err)
		}
		if len(out.Failed) > 0 {
			return fmt.Errorf("messaging: send fifo batch %s: %d failed (%s)", queue, len(out.Failed), aws.ToString(out.Failed[0].Message))
		}
	}
	return nil
}

// SendRaw は任意の本文を標準キューに送る（scheduler キューへの投入用）。
func SendRaw(ctx context.Context, c *sqs.Client, queueURL, body string) error {
	if _, err := c.SendMessage(ctx, &sqs.SendMessageInput{QueueUrl: aws.String(queueURL), MessageBody: aws.String(body)}); err != nil {
		return fmt.Errorf("messaging: send %s: %w", queueURL, err)
	}
	return nil
}
