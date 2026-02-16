package controller

import (
	"github.com/gogf/gf/v2/net/ghttp"

	"github.com/dublyo/mailat/api/internal/middleware"
	"github.com/dublyo/mailat/api/internal/model"
	"github.com/dublyo/mailat/api/internal/service"
	"github.com/dublyo/mailat/api/pkg/response"
)

type DomainController struct {
	domainService *service.DomainService
}

func NewDomainController(domainService *service.DomainService) *DomainController {
	return &DomainController{domainService: domainService}
}

// Create adds a new domain
// POST /api/v1/domains
func (c *DomainController) Create(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	var req model.CreateDomainRequest
	if err := r.Parse(&req); err != nil {
		response.BadRequest(r, err.Error())
		return
	}

	domain, err := c.domainService.CreateDomain(r.Context(), claims.OrgID, &req)
	if err != nil {
		response.BadRequest(r, err.Error())
		return
	}

	// Get DNS records
	records, _ := c.domainService.GetDNSRecords(r.Context(), domain.ID)

	response.SuccessWithMessage(r, "Domain created. Please add the DNS records shown below.", map[string]interface{}{
		"domain":     domain,
		"dnsRecords": records,
	})
}

// List returns all domains for the organization
// GET /api/v1/domains
func (c *DomainController) List(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	domains, err := c.domainService.ListDomains(r.Context(), claims.OrgID)
	if err != nil {
		response.InternalError(r, err.Error())
		return
	}

	// Include DNS records for each domain
	result := make([]map[string]interface{}, 0, len(domains))
	for _, domain := range domains {
		records, _ := c.domainService.GetDNSRecords(r.Context(), domain.ID)
		result = append(result, map[string]interface{}{
			"id":                domain.ID,
			"uuid":              domain.UUID,
			"orgId":             domain.OrgID,
			"name":              domain.Name,
			"status":            domain.Status,
			"verificationToken": domain.VerificationToken,
			"dkimSelector":      domain.DKIMSelector,
			"dkimPublicKey":     domain.DKIMPublicKey,
			"emailProvider":     domain.EmailProvider,
			"sesVerified":       domain.SESVerified,
			"mxVerified":        domain.MXVerified,
			"spfVerified":       domain.SPFVerified,
			"dkimVerified":      domain.DKIMVerified,
			"dmarcVerified":     domain.DMARCVerified,
			"receivingEnabled":  domain.ReceivingEnabled,
			"createdAt":         domain.CreatedAt,
			"updatedAt":         domain.UpdatedAt,
			"dnsRecords":        records,
		})
	}

	response.Success(r, result)
}

// Get returns a single domain with DNS records
// GET /api/v1/domains/:uuid
func (c *DomainController) Get(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	domainUUID := r.Get("uuid").String()
	if domainUUID == "" {
		response.BadRequest(r, "Domain UUID required")
		return
	}

	domain, err := c.domainService.GetDomain(r.Context(), claims.OrgID, domainUUID)
	if err != nil {
		response.NotFound(r, err.Error())
		return
	}

	records, _ := c.domainService.GetDNSRecords(r.Context(), domain.ID)

	response.Success(r, map[string]interface{}{
		"domain":     domain,
		"dnsRecords": records,
	})
}

// Verify checks DNS records and updates verification status
// POST /api/v1/domains/:uuid/verify
func (c *DomainController) Verify(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	domainUUID := r.Get("uuid").String()
	if domainUUID == "" {
		response.BadRequest(r, "Domain UUID required")
		return
	}

	domain, err := c.domainService.GetDomain(r.Context(), claims.OrgID, domainUUID)
	if err != nil {
		response.NotFound(r, err.Error())
		return
	}

	results, err := c.domainService.VerifyDNS(r.Context(), domain.ID)
	if err != nil {
		response.InternalError(r, err.Error())
		return
	}

	// Reload domain to get updated status
	domain, _ = c.domainService.GetDomain(r.Context(), claims.OrgID, domainUUID)
	records, _ := c.domainService.GetDNSRecords(r.Context(), domain.ID)

	response.Success(r, map[string]interface{}{
		"domain":              domain,
		"dnsRecords":          records,
		"verificationResults": results,
	})
}

// Delete removes a domain
// DELETE /api/v1/domains/:uuid
func (c *DomainController) Delete(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	domainUUID := r.Get("uuid").String()
	if domainUUID == "" {
		response.BadRequest(r, "Domain UUID required")
		return
	}

	err := c.domainService.DeleteDomain(r.Context(), claims.OrgID, domainUUID)
	if err != nil {
		response.NotFound(r, err.Error())
		return
	}

	response.SuccessWithMessage(r, "Domain deleted", nil)
}

// InitiateSES registers domain with AWS SES and returns DKIM records
// POST /api/v1/domains/:uuid/ses-verify
func (c *DomainController) InitiateSES(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	domainUUID := r.Get("uuid").String()
	if domainUUID == "" {
		response.BadRequest(r, "Domain UUID required")
		return
	}

	domain, err := c.domainService.GetDomain(r.Context(), claims.OrgID, domainUUID)
	if err != nil {
		response.NotFound(r, err.Error())
		return
	}

	// Initiate SES verification
	sesRecords, err := c.domainService.InitiateSESVerification(r.Context(), domain.ID)
	if err != nil {
		response.BadRequest(r, err.Error())
		return
	}

	// Reload domain to get updated status
	domain, _ = c.domainService.GetDomain(r.Context(), claims.OrgID, domainUUID)
	records, _ := c.domainService.GetDNSRecords(r.Context(), domain.ID)

	response.SuccessWithMessage(r, "SES verification initiated. Add the DKIM records to your DNS.", map[string]interface{}{
		"domain":     domain,
		"dnsRecords": records,
		"sesRecords": sesRecords,
	})
}

// CheckSESStatus checks the SES verification status
// GET /api/v1/domains/:uuid/ses-status
func (c *DomainController) CheckSESStatus(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	domainUUID := r.Get("uuid").String()
	if domainUUID == "" {
		response.BadRequest(r, "Domain UUID required")
		return
	}

	domain, err := c.domainService.GetDomain(r.Context(), claims.OrgID, domainUUID)
	if err != nil {
		response.NotFound(r, err.Error())
		return
	}

	status, err := c.domainService.CheckSESVerificationStatus(r.Context(), domain.ID)
	if err != nil {
		response.BadRequest(r, err.Error())
		return
	}

	// Reload domain to get updated status
	domain, _ = c.domainService.GetDomain(r.Context(), claims.OrgID, domainUUID)

	response.Success(r, map[string]interface{}{
		"domain":    domain,
		"sesStatus": status,
	})
}

// AddDNSToCloudflare adds required DNS records to Cloudflare
// POST /api/v1/domains/:uuid/dns/cloudflare
func (c *DomainController) AddDNSToCloudflare(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	domainUUID := r.Get("uuid").String()
	if domainUUID == "" {
		response.BadRequest(r, "Domain UUID required")
		return
	}

	var req struct {
		APIToken string `json:"apiToken"`
		ZoneID   string `json:"zoneId"`
	}
	if err := r.Parse(&req); err != nil {
		response.BadRequest(r, err.Error())
		return
	}

	if req.APIToken == "" {
		response.BadRequest(r, "Cloudflare API token is required")
		return
	}

	domain, err := c.domainService.GetDomain(r.Context(), claims.OrgID, domainUUID)
	if err != nil {
		response.NotFound(r, err.Error())
		return
	}

	// Add DNS records to Cloudflare
	results, err := c.domainService.AddDNSToCloudflare(r.Context(), domain.ID, req.APIToken, req.ZoneID)
	if err != nil {
		response.BadRequest(r, err.Error())
		return
	}

	response.SuccessWithMessage(r, "DNS records added to Cloudflare", map[string]interface{}{
		"results": results,
	})
}

// GetCloudflareZones lists Cloudflare zones for the API token
// POST /api/v1/domains/cloudflare/zones
func (c *DomainController) GetCloudflareZones(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Not authenticated")
		return
	}

	var req struct {
		APIToken string `json:"apiToken"`
	}
	if err := r.Parse(&req); err != nil {
		response.BadRequest(r, err.Error())
		return
	}

	if req.APIToken == "" {
		response.BadRequest(r, "Cloudflare API token is required")
		return
	}

	zones, err := c.domainService.GetCloudflareZones(r.Context(), req.APIToken)
	if err != nil {
		response.BadRequest(r, err.Error())
		return
	}

	response.Success(r, zones)
}
