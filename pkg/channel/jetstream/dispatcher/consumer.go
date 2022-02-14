package dispatcher

import (
	"context"
	"errors"
	cejs "github.com/cloudevents/sdk-go/protocol/nats_jetstream/v2"
	"github.com/cloudevents/sdk-go/v2/binding"
	"github.com/nats-io/nats.go"
	"go.uber.org/zap"
	eventingchannels "knative.dev/eventing/pkg/channel"
	"knative.dev/eventing/pkg/channel/fanout"
	"knative.dev/eventing/pkg/kncloudevents"
	"sync"
)

var (
	ErrConsumerClosed = errors.New("dispatcher consumer closed")
)

type Consumer struct {
	sub              Subscription
	dispatcher       eventingchannels.MessageDispatcher
	reporter         eventingchannels.StatsReporter
	channelNamespace string

	jsSub *nats.Subscription

	logger *zap.SugaredLogger
	ctx    context.Context

	mu     sync.Mutex
	closed bool
}

func (c *Consumer) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return ErrConsumerClosed
	}

	c.closed = true

	// TODO: should we wait for messages to finish and c.jsSub.IsValid() to return false?
	return c.jsSub.Drain()
}

func (c *Consumer) MsgHandler(msg *nats.Msg) {
	if err := c.doHandle(msg); err != nil {
		c.logger.Errorw("failed to handle message", zap.Error(err))
		return
	}

	if err := msg.Ack(); err != nil {
		c.logger.Errorw("failed to Ack message after successful delivery to subscriber", zap.Error(err))
		return
	}
}

func (c *Consumer) doHandle(msg *nats.Msg) error {
	logger := c.logger.With(zap.String("msg_id", msg.Header.Get(nats.MsgIdHdr)))
	logger.Debugw("received message from JetStream consumer")
	message := cejs.NewMessage(msg)
	if message.ReadEncoding() == binding.EncodingUnknown {
		return errors.New("received a message with unknown encoding")
	}

	te := kncloudevents.TypeExtractorTransformer("")

	dispatchExecutionInfo, err := c.dispatcher.DispatchMessageWithRetries(
		c.ctx,
		message,
		nil,
		c.sub.Subscriber,
		c.sub.Reply,
		c.sub.DeadLetter,
		c.sub.RetryConfig,
		&te,
	)

	args := eventingchannels.ReportArgs{
		Ns:        c.channelNamespace,
		EventType: string(te),
	}
	_ = fanout.ParseDispatchResultAndReportMetrics(fanout.NewDispatchResult(err, dispatchExecutionInfo), c.reporter, args)

	logger.Debugw("message forward to downstream subscriber")
	return err
}
