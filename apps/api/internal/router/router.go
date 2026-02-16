package router

import (
	"net/http"

	"github.com/gogf/gf/v2/net/ghttp"

	"github.com/dublyo/mailat/api/internal/controller"
	"github.com/dublyo/mailat/api/internal/handler"
	"github.com/dublyo/mailat/api/internal/middleware"
	"github.com/dublyo/mailat/api/internal/service"
	"github.com/dublyo/mailat/api/internal/config"
	"github.com/dublyo/mailat/api/internal/database"
)

const swaggerUIHTML = `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width, initial-scale=1.0">
  <title>mailat.co API Documentation</title>
  <link rel="stylesheet" type="text/css" href="https://unpkg.com/swagger-ui-dist@5/swagger-ui.css">
  <style>
    html { box-sizing: border-box; overflow: -moz-scrollbars-vertical; overflow-y: scroll; }
    *, *:before, *:after { box-sizing: inherit; }
    body { margin: 0; background: #fafafa; }
    .swagger-ui .topbar { display: none; }
    .swagger-ui .info { margin: 20px 0; }
    .swagger-ui .info .title { font-size: 36px; }
  </style>
</head>
<body>
  <div id="swagger-ui"></div>
  <script src="https://unpkg.com/swagger-ui-dist@5/swagger-ui-bundle.js"></script>
  <script src="https://unpkg.com/swagger-ui-dist@5/swagger-ui-standalone-preset.js"></script>
  <script>
    window.onload = function() {
      window.ui = SwaggerUIBundle({
        url: "/docs/openapi.yaml",
        dom_id: '#swagger-ui',
        deepLinking: true,
        presets: [
          SwaggerUIBundle.presets.apis,
          SwaggerUIStandalonePreset
        ],
        plugins: [
          SwaggerUIBundle.plugins.DownloadUrl
        ],
        layout: "StandaloneLayout",
        validatorUrl: null
      });
    };
  </script>
</body>
</html>`

func Setup(s *ghttp.Server, cfg *config.Config) {
	// Initialize services
	authService := service.NewAuthService(database.DB, cfg)
	domainService := service.NewDomainService(database.DB, cfg)
	identityService := service.NewIdentityService(database.DB, cfg)
	inboxService := service.NewInboxService(database.DB, cfg, identityService)
	composeService := service.NewComposeService(database.DB, cfg, identityService)
	transactionalService := service.NewTransactionalService(database.DB, cfg, database.Redis)
	webhookService := service.NewWebhookService(database.DB, cfg)
	contactService := service.NewContactService(database.DB, cfg)
	listService := service.NewListService(database.DB, cfg)
	campaignService := service.NewCampaignService(database.DB, cfg, database.Redis)
	automationService := service.NewAutomationService(database.DB, cfg)
	trackingService := service.NewTrackingService(database.DB, cfg)
	complianceService := service.NewComplianceService(database.DB, cfg)
	healthOpsService := service.NewHealthService(database.DB, cfg)
	emailRulesService := service.NewEmailRulesService(database.DB, cfg)
	autoReplyService := service.NewAutoReplyService(database.DB, cfg)
	twoFactorService := service.NewTwoFactorService(database.DB, cfg)
	auditLogService := service.NewAuditLogService(database.DB, cfg)
	oauthService := service.NewOAuthService(database.DB, cfg)
	sessionService := service.NewSessionService(database.DB, cfg)
	settingsService := service.NewSettingsService(database.DB, cfg)

	// Phase 5 additional services
	webauthnService := service.NewWebAuthnService(database.DB, cfg)
	sharedMailboxService := service.NewSharedMailboxService(database.DB, cfg)
	sieveService := service.NewSieveService(database.DB, cfg)
	webhookTriggerService := service.NewWebhookTriggerService(database.DB, cfg)
	pushService := service.NewPushNotificationService(database.DB, cfg)
	brandingService := service.NewBrandingService(database.DB, cfg)

	// Email Receiving service
	receivingService, _ := service.NewReceivingService(
		database.DB,
		cfg.AWSRegion,
		cfg.AWSAccessKeyID,
		cfg.AWSSecretAccessKey,
		cfg.APIUrl,
	)

	// Initialize controllers
	healthCtrl := controller.NewHealthController()
	trackingCtrl := controller.NewTrackingController(trackingService)
	complianceCtrl := controller.NewComplianceController(complianceService)
	authCtrl := controller.NewAuthController(authService)
	domainCtrl := controller.NewDomainController(domainService)
	identityCtrl := controller.NewIdentityController(identityService)
	inboxCtrl := controller.NewInboxController(inboxService)
	composeCtrl := controller.NewComposeController(composeService)
	transactionalCtrl := controller.NewTransactionalController(transactionalService)
	webhookCtrl := controller.NewWebhookController(webhookService)
	contactCtrl := controller.NewContactController(contactService)
	listCtrl := controller.NewListController(listService)
	campaignCtrl := controller.NewCampaignController(campaignService)
	automationCtrl := controller.NewAutomationController(automationService)
	healthOpsCtrl := controller.NewHealthOpsController(healthOpsService)
	emailRulesCtrl := controller.NewEmailRulesController(emailRulesService, autoReplyService)
	securityCtrl := controller.NewSecurityController(twoFactorService, auditLogService, sessionService)
	oauthCtrl := controller.NewOAuthController(oauthService, auditLogService, cfg)
	phase5Ctrl := controller.NewPhase5Controller(webauthnService, sharedMailboxService, sieveService, webhookTriggerService, pushService, brandingService, auditLogService)
	settingsCtrl := controller.NewSettingsController(settingsService)

	// AWS Setup handler
	awsSetupHandler := handler.NewAWSSetupHandler(database.DB, cfg)

	// Email Receiving controllers
	sseCtrl := controller.NewSSEController()
	sesWebhookCtrl := controller.NewSESWebhookController(receivingService)
	receivedInboxCtrl := controller.NewReceivedInboxController(inboxService, receivingService)

	// CORS middleware
	s.Use(ghttp.MiddlewareCORS)

	// Swagger/OpenAPI documentation
	s.Group("/docs", func(group *ghttp.RouterGroup) {
		// Serve OpenAPI spec
		group.GET("/openapi.yaml", func(r *ghttp.Request) {
			r.Response.Header().Set("Content-Type", "application/x-yaml")
			r.Response.Header().Set("Access-Control-Allow-Origin", "*")
			http.ServeFile(r.Response.Writer, r.Request, "docs/openapi.yaml")
		})
		group.GET("/openapi.json", func(r *ghttp.Request) {
			r.Response.Header().Set("Content-Type", "application/json")
			r.Response.Header().Set("Access-Control-Allow-Origin", "*")
			http.ServeFile(r.Response.Writer, r.Request, "docs/openapi.json")
		})
		// Swagger UI using CDN
		group.GET("/", func(r *ghttp.Request) {
			r.Response.Header().Set("Content-Type", "text/html")
			r.Response.Write(swaggerUIHTML)
		})
	})

	// API v1 routes
	s.Group("/api/v1", func(group *ghttp.RouterGroup) {
		// Health check (public)
		group.GET("/health", healthCtrl.Health)
		group.GET("/ready", healthCtrl.Ready)

		// Tracking routes (public - no auth)
		group.GET("/tracking/open/:token", trackingCtrl.TrackOpen)
		group.GET("/tracking/click/:token", trackingCtrl.TrackClick)

		// Compliance routes (public - no auth)
		group.GET("/unsubscribe/:token", complianceCtrl.GetUnsubscribePage)
		group.POST("/unsubscribe/:token", complianceCtrl.OneClickUnsubscribe)
		group.DELETE("/unsubscribe/:token", complianceCtrl.ConfirmUnsubscribe)
		group.GET("/preferences/:token", complianceCtrl.GetPreferences)
		group.PUT("/preferences/:token", complianceCtrl.UpdatePreferences)
		group.GET("/confirm/:token", complianceCtrl.ConfirmDoubleOptIn)

		// Email forward verification (public - clicked from email)
		group.POST("/forwards/:id/verify", emailRulesCtrl.VerifyEmailForward)

		// SES Webhook (public - called by AWS SNS)
		group.POST("/webhooks/ses/incoming", sesWebhookCtrl.HandleIncoming)

		// OAuth2 routes (public - for login/callback flow)
		group.GET("/oauth/providers", oauthCtrl.GetProviders)
		group.GET("/oauth/:provider", oauthCtrl.InitiateOAuth)
		group.GET("/oauth/:provider/callback", oauthCtrl.HandleCallback)

		// Auth routes (public)
		group.Group("/auth", func(authGroup *ghttp.RouterGroup) {
			authGroup.GET("/register-status", authCtrl.RegisterStatus)
			authGroup.POST("/register", authCtrl.Register)
			authGroup.POST("/login", authCtrl.Login)

			// Protected auth routes
			authGroup.Middleware(middleware.Auth)
			authGroup.GET("/me", authCtrl.Me)

			// Session management (alias for /security/sessions)
			authGroup.GET("/sessions", securityCtrl.ListSessions)
			authGroup.DELETE("/sessions/:uuid", securityCtrl.RevokeSession)
			authGroup.POST("/sessions/revoke-all", securityCtrl.RevokeAllSessions)

			// Password change
			authGroup.POST("/change-password", securityCtrl.ChangePassword)

			// 2FA (alias for /security/2fa)
			authGroup.POST("/2fa/enable", securityCtrl.Setup2FA)
			authGroup.POST("/2fa/verify", securityCtrl.Verify2FA)
			authGroup.POST("/2fa/disable", securityCtrl.Disable2FA)
		})

		// Protected routes
		group.Group("/", func(protectedGroup *ghttp.RouterGroup) {
			protectedGroup.Middleware(middleware.Auth)

			// User Settings
			protectedGroup.GET("/settings", settingsCtrl.GetSettings)
			protectedGroup.PUT("/settings", settingsCtrl.UpdateSettings)

			// AWS Setup
			protectedGroup.POST("/settings/aws/validate", awsSetupHandler.ValidateCredentials)
			protectedGroup.POST("/settings/aws/provision", awsSetupHandler.ProvisionResources)

			// SSE (Server-Sent Events)
			protectedGroup.GET("/sse/connect", sseCtrl.Connect)

			// Received Inbox
			protectedGroup.GET("/inbox/received", receivedInboxCtrl.ListEmails)
			protectedGroup.GET("/inbox/received/counts", receivedInboxCtrl.GetCounts)
			protectedGroup.GET("/inbox/received/:uuid", receivedInboxCtrl.GetEmail)
			protectedGroup.POST("/inbox/received/mark", receivedInboxCtrl.MarkEmails)
			protectedGroup.POST("/inbox/received/star", receivedInboxCtrl.StarEmails)
			protectedGroup.POST("/inbox/received/move", receivedInboxCtrl.MoveEmails)
			protectedGroup.POST("/inbox/received/trash", receivedInboxCtrl.TrashEmails)
			protectedGroup.POST("/inbox/setup", receivedInboxCtrl.SetupReceiving)
			protectedGroup.POST("/identities/:uuid/catch-all", receivedInboxCtrl.SetCatchAll)

			// API Keys
			protectedGroup.POST("/api-keys", authCtrl.CreateAPIKey)
			protectedGroup.GET("/api-keys", authCtrl.ListAPIKeys)
			protectedGroup.DELETE("/api-keys/:uuid", authCtrl.DeleteAPIKey)

			// Domains
			protectedGroup.POST("/domains", domainCtrl.Create)
			protectedGroup.GET("/domains", domainCtrl.List)
			protectedGroup.GET("/domains/:uuid", domainCtrl.Get)
			protectedGroup.POST("/domains/:uuid/verify", domainCtrl.Verify)
			protectedGroup.DELETE("/domains/:uuid", domainCtrl.Delete)
			// SES and Cloudflare integration
			protectedGroup.POST("/domains/:uuid/ses-verify", domainCtrl.InitiateSES)
			protectedGroup.GET("/domains/:uuid/ses-status", domainCtrl.CheckSESStatus)
			protectedGroup.POST("/domains/:uuid/dns/cloudflare", domainCtrl.AddDNSToCloudflare)
			protectedGroup.POST("/domains/cloudflare/zones", domainCtrl.GetCloudflareZones)

			// Identities
			protectedGroup.POST("/identities", identityCtrl.Create)
			protectedGroup.GET("/identities", identityCtrl.List)
			protectedGroup.GET("/identities/:uuid", identityCtrl.Get)
			protectedGroup.PUT("/identities/:uuid/password", identityCtrl.UpdatePassword)
			protectedGroup.DELETE("/identities/:uuid", identityCtrl.Delete)

			// Unified Inbox
			protectedGroup.GET("/inbox", inboxCtrl.GetInbox)
			protectedGroup.GET("/inbox/mailboxes", inboxCtrl.GetMailboxes)
			protectedGroup.GET("/inbox/emails/:id", inboxCtrl.GetEmail)
			protectedGroup.GET("/inbox/threads/:id", inboxCtrl.GetThread)
			protectedGroup.GET("/inbox/search", inboxCtrl.Search)
			protectedGroup.POST("/inbox/mark-read", inboxCtrl.MarkRead)
			protectedGroup.POST("/inbox/toggle-flag", inboxCtrl.ToggleFlag)
			protectedGroup.POST("/inbox/move", inboxCtrl.MoveEmails)
			protectedGroup.POST("/inbox/delete", inboxCtrl.DeleteEmails)

			// Compose & Reply
			protectedGroup.POST("/compose/send", composeCtrl.SendEmail)
			protectedGroup.POST("/compose/drafts", composeCtrl.SaveDraft)
			protectedGroup.PUT("/compose/drafts/:id", composeCtrl.UpdateDraft)
			protectedGroup.DELETE("/compose/drafts/:id", composeCtrl.DeleteDraft)
			protectedGroup.GET("/compose/reply/:id", composeCtrl.GetReplyContext)
			protectedGroup.GET("/compose/forward/:id", composeCtrl.GetForwardContext)
			protectedGroup.POST("/compose/attachments", composeCtrl.UploadAttachment)

			// Transactional Email API (Phase 2)
			protectedGroup.POST("/emails", transactionalCtrl.SendEmail)
			protectedGroup.POST("/emails/batch", transactionalCtrl.BatchSendEmail)
			protectedGroup.GET("/emails/:id", transactionalCtrl.GetEmailStatus)
			protectedGroup.DELETE("/emails/:id", transactionalCtrl.CancelEmail)

			// Email Templates
			protectedGroup.POST("/templates", transactionalCtrl.CreateTemplate)
			protectedGroup.GET("/templates", transactionalCtrl.ListTemplates)
			protectedGroup.GET("/templates/:uuid", transactionalCtrl.GetTemplate)
			protectedGroup.PUT("/templates/:uuid", transactionalCtrl.UpdateTemplate)
			protectedGroup.DELETE("/templates/:uuid", transactionalCtrl.DeleteTemplate)
			protectedGroup.POST("/templates/:uuid/preview", transactionalCtrl.PreviewTemplate)

			// Webhooks
			protectedGroup.POST("/webhooks", webhookCtrl.CreateWebhook)
			protectedGroup.GET("/webhooks", webhookCtrl.ListWebhooks)
			protectedGroup.GET("/webhooks/:uuid", webhookCtrl.GetWebhook)
			protectedGroup.PUT("/webhooks/:uuid", webhookCtrl.UpdateWebhook)
			protectedGroup.DELETE("/webhooks/:uuid", webhookCtrl.DeleteWebhook)
			protectedGroup.POST("/webhooks/:uuid/rotate-secret", webhookCtrl.RotateSecret)
			protectedGroup.GET("/webhooks/:uuid/calls", webhookCtrl.GetWebhookCalls)
			protectedGroup.POST("/webhooks/:uuid/test", webhookCtrl.TestWebhook)

			// Phase 3: Marketing - Contacts
			protectedGroup.POST("/contacts", contactCtrl.Create)
			protectedGroup.GET("/contacts", contactCtrl.List)
			protectedGroup.GET("/contacts/:uuid", contactCtrl.Get)
			protectedGroup.PUT("/contacts/:uuid", contactCtrl.Update)
			protectedGroup.DELETE("/contacts/:uuid", contactCtrl.Delete)
			protectedGroup.POST("/contacts/import", contactCtrl.Import)
			protectedGroup.POST("/contacts/export", contactCtrl.Export)
			protectedGroup.POST("/contacts/unsubscribe", contactCtrl.Unsubscribe)
			protectedGroup.GET("/contacts/:uuid/export", complianceCtrl.ExportContactData)
			protectedGroup.DELETE("/contacts/:uuid/gdpr", complianceCtrl.DeleteContactData)
			protectedGroup.GET("/contacts/:uuid/consent-audit", complianceCtrl.GetConsentAuditTrail)

			// Phase 3: Marketing - Lists
			protectedGroup.POST("/lists", listCtrl.Create)
			protectedGroup.GET("/lists", listCtrl.List)
			protectedGroup.GET("/lists/:uuid", listCtrl.Get)
			protectedGroup.PUT("/lists/:uuid", listCtrl.Update)
			protectedGroup.DELETE("/lists/:uuid", listCtrl.Delete)
			protectedGroup.POST("/lists/:uuid/contacts", listCtrl.AddContacts)
			protectedGroup.DELETE("/lists/:uuid/contacts", listCtrl.RemoveContacts)
			protectedGroup.GET("/lists/:uuid/contacts", listCtrl.GetListContacts)
			protectedGroup.POST("/lists/:uuid/contacts/import", listCtrl.ImportContactsToList)
			protectedGroup.POST("/lists/:uuid/contacts/manual", listCtrl.ManualAddContactToList)

			// Phase 3: Marketing - Campaigns
			protectedGroup.POST("/campaigns", campaignCtrl.Create)
			protectedGroup.GET("/campaigns", campaignCtrl.List)
			protectedGroup.GET("/campaigns/:uuid", campaignCtrl.Get)
			protectedGroup.PUT("/campaigns/:uuid", campaignCtrl.Update)
			protectedGroup.DELETE("/campaigns/:uuid", campaignCtrl.Delete)
			protectedGroup.POST("/campaigns/:uuid/schedule", campaignCtrl.Schedule)
			protectedGroup.POST("/campaigns/:uuid/send", campaignCtrl.SendNow)
			protectedGroup.POST("/campaigns/:uuid/pause", campaignCtrl.Pause)
			protectedGroup.POST("/campaigns/:uuid/resume", campaignCtrl.Resume)
			protectedGroup.POST("/campaigns/:uuid/cancel", campaignCtrl.Cancel)
			protectedGroup.GET("/campaigns/:uuid/stats", campaignCtrl.GetStats)
			protectedGroup.POST("/campaigns/:uuid/preview", campaignCtrl.Preview)
			protectedGroup.POST("/campaigns/:uuid/test", campaignCtrl.SendTest)

			// Automations/Workflows
			protectedGroup.POST("/automations", automationCtrl.Create)
			protectedGroup.GET("/automations", automationCtrl.List)
			protectedGroup.GET("/automations/:uuid", automationCtrl.Get)
			protectedGroup.PUT("/automations/:uuid", automationCtrl.Update)
			protectedGroup.DELETE("/automations/:uuid", automationCtrl.Delete)
			protectedGroup.POST("/automations/:uuid/activate", automationCtrl.Activate)
			protectedGroup.POST("/automations/:uuid/pause", automationCtrl.Pause)
			protectedGroup.GET("/automations/:uuid/stats", automationCtrl.GetStats)
			protectedGroup.POST("/automations/:uuid/enroll", automationCtrl.EnrollContact)

			// Phase 4: Health & Operations
			protectedGroup.POST("/health/blacklist-check", healthOpsCtrl.CheckBlacklists)
			protectedGroup.GET("/health/reputation", healthOpsCtrl.GetReputationMetrics)
			protectedGroup.GET("/health/ses-limits", healthOpsCtrl.GetSESLimits)
			protectedGroup.GET("/health/summary", healthOpsCtrl.GetEmailHealthSummary)
			protectedGroup.GET("/health/warmup/schedules", healthOpsCtrl.GetWarmupSchedules)
			protectedGroup.POST("/health/warmup", healthOpsCtrl.StartWarmup)
			protectedGroup.GET("/health/warmup/:ip", healthOpsCtrl.GetWarmupStatus)
			protectedGroup.GET("/health/quota", healthOpsCtrl.GetQuotaStatus)
			protectedGroup.GET("/health/alerts", healthOpsCtrl.GetAlerts)
			protectedGroup.POST("/health/alerts/:id/acknowledge", healthOpsCtrl.AcknowledgeAlert)
			protectedGroup.GET("/health/logs", healthOpsCtrl.GetDeliveryLogs)

			// Phase 5.1: Email Rules & Filters
			protectedGroup.POST("/rules", emailRulesCtrl.CreateRule)
			protectedGroup.GET("/rules", emailRulesCtrl.ListRules)
			protectedGroup.GET("/rules/:id", emailRulesCtrl.GetRule)
			protectedGroup.PUT("/rules/:id", emailRulesCtrl.UpdateRule)
			protectedGroup.DELETE("/rules/:id", emailRulesCtrl.DeleteRule)
			protectedGroup.POST("/rules/reorder", emailRulesCtrl.ReorderRules)
			protectedGroup.POST("/rules/:id/test", emailRulesCtrl.TestRule)

			// Phase 5.1: Auto-Reply / Vacation Responder
			protectedGroup.POST("/auto-replies", emailRulesCtrl.CreateAutoReply)
			protectedGroup.GET("/auto-replies", emailRulesCtrl.ListAutoReplies)
			protectedGroup.GET("/auto-replies/:id", emailRulesCtrl.GetAutoReply)
			protectedGroup.PUT("/auto-replies/:id", emailRulesCtrl.UpdateAutoReply)
			protectedGroup.DELETE("/auto-replies/:id", emailRulesCtrl.DeleteAutoReply)

			// Phase 5.1: Email Forwarding
			protectedGroup.POST("/forwards", emailRulesCtrl.CreateEmailForward)
			protectedGroup.GET("/forwards", emailRulesCtrl.ListEmailForwards)
			protectedGroup.DELETE("/forwards/:id", emailRulesCtrl.DeleteEmailForward)

			// Phase 5.2: Two-Factor Authentication
			protectedGroup.POST("/security/2fa/setup", securityCtrl.Setup2FA)
			protectedGroup.POST("/security/2fa/verify", securityCtrl.Verify2FA)
			protectedGroup.POST("/security/2fa/disable", securityCtrl.Disable2FA)
			protectedGroup.POST("/security/2fa/backup-codes", securityCtrl.RegenerateBackupCodes)
			protectedGroup.GET("/security/2fa/status", securityCtrl.Get2FAStatus)

			// Phase 5.2: Audit Logs
			protectedGroup.GET("/security/audit-logs", securityCtrl.ListAuditLogs)
			protectedGroup.GET("/security/events", securityCtrl.GetSecurityEvents)
			protectedGroup.GET("/security/my-activity", securityCtrl.GetMyActivity)

			// Phase 5.2: Session Management
			protectedGroup.GET("/security/sessions", securityCtrl.ListSessions)
			protectedGroup.DELETE("/security/sessions/:uuid", securityCtrl.RevokeSession)
			protectedGroup.POST("/security/sessions/revoke-all", securityCtrl.RevokeAllSessions)

			// Phase 5.3: OAuth2 Connections (protected - for managing connections)
			protectedGroup.GET("/oauth/connections", oauthCtrl.GetConnections)
			protectedGroup.POST("/oauth/:provider/connect", oauthCtrl.ConnectProvider)
			protectedGroup.DELETE("/oauth/:provider", oauthCtrl.DisconnectProvider)

			// Phase 5.2: WebAuthn/FIDO2 Security Keys
			protectedGroup.POST("/security/webauthn/register/begin", phase5Ctrl.BeginWebAuthnRegistration)
			protectedGroup.POST("/security/webauthn/register/finish", phase5Ctrl.FinishWebAuthnRegistration)
			protectedGroup.POST("/security/webauthn/authenticate/begin", phase5Ctrl.BeginWebAuthnAuthentication)
			protectedGroup.POST("/security/webauthn/authenticate/finish", phase5Ctrl.FinishWebAuthnAuthentication)
			protectedGroup.GET("/security/webauthn/credentials", phase5Ctrl.ListWebAuthnCredentials)
			protectedGroup.DELETE("/security/webauthn/credentials/:uuid", phase5Ctrl.DeleteWebAuthnCredential)

			// Phase 5.1: Shared Mailboxes
			protectedGroup.POST("/shared-mailboxes", phase5Ctrl.CreateSharedMailbox)
			protectedGroup.GET("/shared-mailboxes", phase5Ctrl.ListSharedMailboxes)
			protectedGroup.GET("/shared-mailboxes/:id", phase5Ctrl.GetSharedMailbox)
			protectedGroup.DELETE("/shared-mailboxes/:id", phase5Ctrl.DeleteSharedMailbox)
			protectedGroup.POST("/shared-mailboxes/:id/members", phase5Ctrl.AddSharedMailboxMember)
			protectedGroup.GET("/shared-mailboxes/:id/members", phase5Ctrl.ListSharedMailboxMembers)
			protectedGroup.DELETE("/shared-mailboxes/:id/members/:userId", phase5Ctrl.RemoveSharedMailboxMember)

			// Phase 5.1: Sieve Scripts
			protectedGroup.POST("/sieve-scripts", phase5Ctrl.CreateSieveScript)
			protectedGroup.GET("/sieve-scripts", phase5Ctrl.ListSieveScripts)
			protectedGroup.GET("/sieve-scripts/:id", phase5Ctrl.GetSieveScript)
			protectedGroup.PUT("/sieve-scripts/:id", phase5Ctrl.UpdateSieveScript)
			protectedGroup.DELETE("/sieve-scripts/:id", phase5Ctrl.DeleteSieveScript)
			protectedGroup.POST("/sieve-scripts/validate", phase5Ctrl.ValidateSieveScript)

			// Phase 5.3: Webhook Triggers (Zapier/n8n integration)
			protectedGroup.GET("/webhook-triggers/types", phase5Ctrl.GetWebhookTriggerTypes)
			protectedGroup.POST("/webhook-triggers", phase5Ctrl.CreateWebhookTrigger)
			protectedGroup.GET("/webhook-triggers", phase5Ctrl.ListWebhookTriggers)
			protectedGroup.GET("/webhook-triggers/:id", phase5Ctrl.GetWebhookTrigger)
			protectedGroup.DELETE("/webhook-triggers/:id", phase5Ctrl.DeleteWebhookTrigger)
			protectedGroup.POST("/webhook-triggers/:id/test", phase5Ctrl.TestWebhookTrigger)

			// Phase 5.4: Push Notifications
			protectedGroup.GET("/push/vapid-key", phase5Ctrl.GetVAPIDKey)
			protectedGroup.POST("/push/subscribe", phase5Ctrl.SubscribePush)
			protectedGroup.POST("/push/unsubscribe", phase5Ctrl.UnsubscribePush)
			protectedGroup.GET("/push/subscriptions", phase5Ctrl.ListPushSubscriptions)
			protectedGroup.PUT("/push/subscriptions/:uuid/preferences", phase5Ctrl.UpdatePushPreferences)

			// Phase 5.5: Branding & Multi-tenant
			protectedGroup.GET("/branding", phase5Ctrl.GetBranding)
			protectedGroup.PUT("/branding", phase5Ctrl.UpdateBranding)
			protectedGroup.POST("/branding/verify-domain", phase5Ctrl.VerifyCustomDomain)
			protectedGroup.GET("/branding/css", phase5Ctrl.GetBrandingCSS)
		})
	})
}
