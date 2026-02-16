package worker

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"time"

	"github.com/hibiken/asynq"

	"github.com/dublyo/mailat/api/internal/config"
)

// parseRedisURL parses a Redis URL and returns asynq.RedisClientOpt
// Supports formats: redis://host:port, redis://:pass@host:port, host:port
func parseRedisURL(redisURL string, fallbackPassword string) asynq.RedisClientOpt {
	// Default values
	addr := "localhost:6379"
	password := fallbackPassword

	// Try to parse as URL
	if u, err := url.Parse(redisURL); err == nil && u.Host != "" {
		addr = u.Host
		if u.User != nil {
			if p, ok := u.User.Password(); ok {
				password = p
			}
		}
	} else {
		// Fallback: assume it's just host:port
		addr = redisURL
	}

	return asynq.RedisClientOpt{
		Addr:     addr,
		Password: password,
		DB:       0,
	}
}

// Worker manages the asynq server and handlers
type Worker struct {
	server *asynq.Server
	mux    *asynq.ServeMux
	db     *sql.DB
	cfg    *config.Config
}

// QueueClient is a client for enqueuing tasks
type QueueClient struct {
	client   *asynq.Client
	redisOpt asynq.RedisClientOpt
}

// NewWorker creates a new worker instance
func NewWorker(db *sql.DB, cfg *config.Config) *Worker {
	redisOpt := parseRedisURL(cfg.RedisURL, cfg.RedisPassword)

	// Configure server with queues and concurrency
	server := asynq.NewServer(
		redisOpt,
		asynq.Config{
			// Specify how many concurrent workers to run
			Concurrency: 10,
			// Queue priority: critical > default > low
			Queues: map[string]int{
				"critical": 6,
				"default":  3,
				"low":      1,
			},
			// Retry configuration
			RetryDelayFunc: func(n int, e error, t *asynq.Task) time.Duration {
				// Exponential backoff: 1s, 2s, 4s, 8s, 16s, 32s, 1m, 2m, 5m, 10m
				delays := []time.Duration{
					1 * time.Second,
					2 * time.Second,
					4 * time.Second,
					8 * time.Second,
					16 * time.Second,
					32 * time.Second,
					1 * time.Minute,
					2 * time.Minute,
					5 * time.Minute,
					10 * time.Minute,
				}
				if n < len(delays) {
					return delays[n]
				}
				return 10 * time.Minute
			},
			// Error handler
			ErrorHandler: asynq.ErrorHandlerFunc(func(ctx context.Context, task *asynq.Task, err error) {
				fmt.Printf("Task %s failed: %v\n", task.Type(), err)
			}),
		},
	)

	mux := asynq.NewServeMux()

	return &Worker{
		server: server,
		mux:    mux,
		db:     db,
		cfg:    cfg,
	}
}

// RegisterHandlers registers all task handlers
func (w *Worker) RegisterHandlers() {
	// Create handlers
	emailHandler := NewEmailHandler(w.db, w.cfg)
	campaignHandler := NewCampaignHandler(w.db, w.cfg)
	webhookHandler := NewWebhookHandler(w.db, w.cfg)
	bounceHandler := NewBounceHandler(w.db, w.cfg)
	scheduledHandler := NewScheduledTaskHandler(w.db, w.cfg)

	// Register handlers
	w.mux.HandleFunc(TypeEmailSend, emailHandler.HandleEmailSend)
	w.mux.HandleFunc(TypeCampaignProcess, campaignHandler.HandleCampaignProcess)
	w.mux.HandleFunc(TypeCampaignBatch, campaignHandler.HandleCampaignBatch)
	w.mux.HandleFunc(TypeWebhookDeliver, webhookHandler.HandleWebhookDeliver)
	w.mux.HandleFunc(TypeBounceProcess, bounceHandler.HandleBounceProcess)

	// Register scheduled task handlers
	w.mux.HandleFunc(TypeScheduledBlacklistCheck, scheduledHandler.HandleBlacklistCheck)
	w.mux.HandleFunc(TypeScheduledWarmupAdvance, scheduledHandler.HandleWarmupAdvance)
	w.mux.HandleFunc(TypeScheduledBounceCheck, scheduledHandler.HandleBounceCheck)
	w.mux.HandleFunc(TypeScheduledAlertDigest, scheduledHandler.HandleAlertDigest)

	fmt.Println("Registered task handlers:")
	fmt.Printf("  - %s\n", TypeEmailSend)
	fmt.Printf("  - %s\n", TypeCampaignProcess)
	fmt.Printf("  - %s\n", TypeCampaignBatch)
	fmt.Printf("  - %s\n", TypeWebhookDeliver)
	fmt.Printf("  - %s\n", TypeBounceProcess)
	fmt.Printf("  - %s (scheduled)\n", TypeScheduledBlacklistCheck)
	fmt.Printf("  - %s (scheduled)\n", TypeScheduledWarmupAdvance)
	fmt.Printf("  - %s (scheduled)\n", TypeScheduledBounceCheck)
	fmt.Printf("  - %s (scheduled)\n", TypeScheduledAlertDigest)
}

// Start starts the worker server
func (w *Worker) Start() error {
	fmt.Println("Starting worker server...")
	w.RegisterHandlers()
	return w.server.Start(w.mux)
}

// Shutdown gracefully shuts down the worker
func (w *Worker) Shutdown() {
	fmt.Println("Shutting down worker...")
	w.server.Shutdown()
}

// NewQueueClient creates a new queue client for enqueuing tasks
func NewQueueClient(cfg *config.Config) (*QueueClient, error) {
	redisOpt := parseRedisURL(cfg.RedisURL, cfg.RedisPassword)

	client := asynq.NewClient(redisOpt)

	return &QueueClient{
		client:   client,
		redisOpt: redisOpt,
	}, nil
}

// Close closes the queue client
func (c *QueueClient) Close() error {
	return c.client.Close()
}

// EnqueueEmailSend enqueues an email send task
func (c *QueueClient) EnqueueEmailSend(payload *EmailSendPayload, opts ...asynq.Option) (*asynq.TaskInfo, error) {
	data, err := payload.Marshal()
	if err != nil {
		return nil, fmt.Errorf("failed to marshal payload: %w", err)
	}

	task := asynq.NewTask(TypeEmailSend, data)

	// Default options
	defaultOpts := []asynq.Option{
		asynq.Queue("default"),
		asynq.MaxRetry(3),
		asynq.Timeout(5 * time.Minute),
	}

	// Append custom options
	allOpts := append(defaultOpts, opts...)

	return c.client.Enqueue(task, allOpts...)
}

// EnqueueEmailSendScheduled enqueues an email send task for later
func (c *QueueClient) EnqueueEmailSendScheduled(payload *EmailSendPayload, processAt time.Time) (*asynq.TaskInfo, error) {
	data, err := payload.Marshal()
	if err != nil {
		return nil, fmt.Errorf("failed to marshal payload: %w", err)
	}

	task := asynq.NewTask(TypeEmailSend, data)

	return c.client.Enqueue(task,
		asynq.Queue("default"),
		asynq.MaxRetry(3),
		asynq.Timeout(5*time.Minute),
		asynq.ProcessAt(processAt),
	)
}

// EnqueueWebhookDeliver enqueues a webhook delivery task
func (c *QueueClient) EnqueueWebhookDeliver(payload *WebhookDeliverPayload) (*asynq.TaskInfo, error) {
	data, err := payload.Marshal()
	if err != nil {
		return nil, fmt.Errorf("failed to marshal payload: %w", err)
	}

	task := asynq.NewTask(TypeWebhookDeliver, data)

	return c.client.Enqueue(task,
		asynq.Queue("default"),
		asynq.MaxRetry(5),
		asynq.Timeout(30*time.Second),
	)
}

// EnqueueBounceProcess enqueues a bounce processing task
func (c *QueueClient) EnqueueBounceProcess(payload *BounceProcessPayload) (*asynq.TaskInfo, error) {
	data, err := payload.Marshal()
	if err != nil {
		return nil, fmt.Errorf("failed to marshal payload: %w", err)
	}

	task := asynq.NewTask(TypeBounceProcess, data)

	return c.client.Enqueue(task,
		asynq.Queue("critical"),
		asynq.MaxRetry(3),
		asynq.Timeout(1*time.Minute),
	)
}

// EnqueueCampaignProcess enqueues a campaign processing task
func (c *QueueClient) EnqueueCampaignProcess(payload *CampaignProcessPayload) (*asynq.TaskInfo, error) {
	data, err := payload.Marshal()
	if err != nil {
		return nil, fmt.Errorf("failed to marshal payload: %w", err)
	}

	task := asynq.NewTask(TypeCampaignProcess, data)

	return c.client.Enqueue(task,
		asynq.Queue("default"),
		asynq.MaxRetry(1), // Campaign processing should only be attempted once
		asynq.Timeout(24*time.Hour), // Long timeout for large campaigns
	)
}

// EnqueueCampaignProcessScheduled enqueues a campaign processing task for later
func (c *QueueClient) EnqueueCampaignProcessScheduled(payload *CampaignProcessPayload, processAt time.Time) (*asynq.TaskInfo, error) {
	data, err := payload.Marshal()
	if err != nil {
		return nil, fmt.Errorf("failed to marshal payload: %w", err)
	}

	task := asynq.NewTask(TypeCampaignProcess, data)

	return c.client.Enqueue(task,
		asynq.Queue("default"),
		asynq.MaxRetry(1),
		asynq.Timeout(24*time.Hour),
		asynq.ProcessAt(processAt),
	)
}

// EnqueueCampaignBatch enqueues a campaign batch processing task
func (c *QueueClient) EnqueueCampaignBatch(payload *CampaignBatchPayload) (*asynq.TaskInfo, error) {
	data, err := payload.Marshal()
	if err != nil {
		return nil, fmt.Errorf("failed to marshal payload: %w", err)
	}

	task := asynq.NewTask(TypeCampaignBatch, data)

	return c.client.Enqueue(task,
		asynq.Queue("default"),
		asynq.MaxRetry(3),
		asynq.Timeout(1*time.Hour),
	)
}

// GetQueueInfo returns information about queues
func (c *QueueClient) GetQueueInfo() (map[string]*asynq.QueueInfo, error) {
	inspector := asynq.NewInspector(c.redisOpt)
	defer inspector.Close()

	queues, err := inspector.Queues()
	if err != nil {
		return nil, err
	}

	result := make(map[string]*asynq.QueueInfo)
	for _, q := range queues {
		info, err := inspector.GetQueueInfo(q)
		if err != nil {
			continue
		}
		result[q] = info
	}

	return result, nil
}
