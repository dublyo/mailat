package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/gogf/gf/v2/frame/g"

	"github.com/dublyo/mailat/api/internal/config"
	"github.com/dublyo/mailat/api/internal/database"
	"github.com/dublyo/mailat/api/internal/router"
	"github.com/dublyo/mailat/api/internal/service"
	"github.com/dublyo/mailat/api/internal/worker"
)

// @title mailat.co API
// @version 1.0
// @description Comprehensive all-in-one email platform API for transactional, marketing, and inbox management.
// @termsOfService https://mailat.co/terms

// @contact.name API Support
// @contact.url https://mailat.co/support
// @contact.email support@mailat.co

// @license.name MIT
// @license.url https://opensource.org/licenses/MIT

// @host localhost:8080
// @BasePath /api/v1

// @securityDefinitions.apikey BearerAuth
// @in header
// @name Authorization
// @description Bearer token authentication. Format: "Bearer {token}"

// @tag.name Auth
// @tag.description Authentication and user management endpoints

// @tag.name Inbox
// @tag.description Email inbox management - read, search, organize emails

// @tag.name Compose
// @tag.description Send emails, save drafts, reply and forward

// @tag.name Domains
// @tag.description Domain management and DNS verification

// @tag.name Identities
// @tag.description Email identity/mailbox management

// @tag.name Contacts
// @tag.description Contact management for marketing

// @tag.name Lists
// @tag.description Contact list management

// @tag.name Campaigns
// @tag.description Marketing campaign management

// @tag.name Transactional
// @tag.description Transactional email API

// @tag.name Webhooks
// @tag.description Webhook management for event notifications

// @tag.name Health
// @tag.description Deliverability health and monitoring

// @tag.name Settings
// @tag.description User and organization settings

// @tag.name Security
// @tag.description 2FA, sessions, and security management

func main() {
	// Load configuration
	cfg, err := config.Load()
	if err != nil {
		fmt.Printf("Failed to load config: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Starting mailat.co API server...\n")
	fmt.Printf("Environment: %s\n", cfg.Env)

	// Connect to PostgreSQL
	db, err := database.Connect(cfg)
	if err != nil {
		fmt.Printf("Failed to connect to PostgreSQL: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()
	fmt.Println("Connected to PostgreSQL")

	// Auto-migrate database schema
	if err := database.InitSchema(db); err != nil {
		fmt.Printf("Failed to initialize database schema: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("Database schema initialized")

	// Connect to Redis
	redis, err := database.ConnectRedis(cfg)
	if err != nil {
		fmt.Printf("Failed to connect to Redis: %v\n", err)
		os.Exit(1)
	}
	defer redis.Close()
	fmt.Println("Connected to Redis")

	// Start worker if enabled
	var w *worker.Worker
	var sched *worker.Scheduler
	if cfg.WorkerEnabled {
		w = worker.NewWorker(db, cfg)
		go func() {
			if err := w.Start(); err != nil {
				fmt.Printf("Worker failed: %v\n", err)
			}
		}()
		fmt.Println("Worker started (asynq job queue)")

		// Start scheduler for periodic tasks
		sched, err = worker.NewScheduler(db, cfg)
		if err != nil {
			fmt.Printf("Warning: failed to create scheduler: %v\n", err)
		} else {
			if err := sched.RegisterScheduledTasks(); err != nil {
				fmt.Printf("Warning: failed to register scheduled tasks: %v\n", err)
			} else {
				go func() {
					if err := sched.Start(); err != nil {
						fmt.Printf("Scheduler failed: %v\n", err)
					}
				}()
				fmt.Println("Scheduler started (periodic tasks)")
			}
		}
	}

	// Setup PostgreSQL triggers for delivery tracking
	if err := service.SetupTriggers(db); err != nil {
		fmt.Printf("Warning: failed to setup delivery tracking triggers: %v\n", err)
	}

	// Start delivery tracker (PostgreSQL NOTIFY listener)
	var tracker *service.DeliveryTracker
	tracker, err = service.NewDeliveryTracker(db, cfg)
	if err != nil {
		fmt.Printf("Warning: failed to create delivery tracker: %v\n", err)
	} else {
		if err := tracker.Start(context.Background()); err != nil {
			fmt.Printf("Warning: failed to start delivery tracker: %v\n", err)
		} else {
			fmt.Println("Delivery tracker started (PostgreSQL NOTIFY)")
		}
	}

	// Create GoFrame server
	s := g.Server()
	s.SetPort(cfg.Port)
	s.SetDumpRouterMap(false)

	// Setup routes
	router.Setup(s, cfg)

	// Graceful shutdown
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		<-ctx.Done()
		fmt.Println("\nShutting down server...")
		if tracker != nil {
			tracker.Stop()
		}
		if sched != nil {
			sched.Shutdown()
		}
		if w != nil {
			w.Shutdown()
		}
		s.Shutdown()
	}()

	// Start server
	fmt.Printf("\n╔══════════════════════════════════════════════════════════════╗\n")
	fmt.Printf("║          mailat.co API Server                              ║\n")
	fmt.Printf("╠══════════════════════════════════════════════════════════════╣\n")
	fmt.Printf("║  Server:     http://localhost:%d                            ║\n", cfg.Port)
	fmt.Printf("║  API Docs:   http://localhost:%d/docs/                      ║\n", cfg.Port)
	fmt.Printf("║  OpenAPI:    http://localhost:%d/docs/openapi.yaml          ║\n", cfg.Port)
	fmt.Printf("╠══════════════════════════════════════════════════════════════╣\n")
	fmt.Printf("║  API Endpoints (140+ total):                                 ║\n")
	fmt.Printf("║                                                              ║\n")
	fmt.Printf("║  Auth & Security:                                            ║\n")
	fmt.Printf("║    /api/v1/auth/*        - Login, register, 2FA, sessions    ║\n")
	fmt.Printf("║                                                              ║\n")
	fmt.Printf("║  Transactional Email:                                        ║\n")
	fmt.Printf("║    /api/v1/emails/*      - Send, track, batch emails         ║\n")
	fmt.Printf("║    /api/v1/templates/*   - Email templates management        ║\n")
	fmt.Printf("║                                                              ║\n")
	fmt.Printf("║  Marketing:                                                  ║\n")
	fmt.Printf("║    /api/v1/contacts/*    - Contact management                ║\n")
	fmt.Printf("║    /api/v1/lists/*       - Contact lists                     ║\n")
	fmt.Printf("║    /api/v1/campaigns/*   - Marketing campaigns               ║\n")
	fmt.Printf("║    /api/v1/automations/* - Email automations                 ║\n")
	fmt.Printf("║                                                              ║\n")
	fmt.Printf("║  Inbox:                                                      ║\n")
	fmt.Printf("║    /api/v1/inbox/*       - Mailbox, messages, folders        ║\n")
	fmt.Printf("║    /api/v1/compose/*     - Send, draft, reply, forward       ║\n")
	fmt.Printf("║                                                              ║\n")
	fmt.Printf("║  Infrastructure:                                             ║\n")
	fmt.Printf("║    /api/v1/domains/*     - Domain & DNS verification         ║\n")
	fmt.Printf("║    /api/v1/identities/*  - Email identities/mailboxes        ║\n")
	fmt.Printf("║    /api/v1/webhooks/*    - Event webhooks                    ║\n")
	fmt.Printf("║                                                              ║\n")
	fmt.Printf("║  Analytics & Health:                                         ║\n")
	fmt.Printf("║    /api/v1/analytics/*   - Stats & reports                   ║\n")
	fmt.Printf("║    /api/v1/health/*      - Deliverability monitoring         ║\n")
	fmt.Printf("║    /api/v1/settings/*    - User & org settings               ║\n")
	fmt.Printf("╚══════════════════════════════════════════════════════════════╝\n")

	s.Run()
}
