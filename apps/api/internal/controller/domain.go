package controller

import (
	"errors"

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
		response.BadRequest(r, domainOperationMessage(err))
		return
	}

	// Get DNS records
	records, err := c.domainService.GetDNSRecords(r.Context(), domain.ID)
	if err != nil {
		response.InternalError(r, "Unable to load domain DNS records")
		return
	}

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
		response.InternalError(r, "Unable to load domain data")
		return
	}

	// Include DNS records for each domain
	result := make([]map[string]interface{}, 0, len(domains))
	for _, domain := range domains {
		records, err := c.domainService.GetDNSRecords(r.Context(), domain.ID)
		if err != nil {
			response.InternalError(r, "Unable to load domain DNS records")
			return
		}
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
		domainReadError(r, err)
		return
	}

	records, err := c.domainService.GetDNSRecords(r.Context(), domain.ID)
	if err != nil {
		response.InternalError(r, "Unable to load domain DNS records")
		return
	}

	response.Success(r, map[string]interface{}{
		"domain":     domain,
		"dnsRecords": records,
	})
}

// DMARC inspects the current policy without changing DNS or database state.
// GET /api/v1/domains/:uuid/dmarc
func (c *DomainController) DMARC(r *ghttp.Request) {
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
	inspection, err := c.domainService.GetDMARC(r.Context(), claims.OrgID, domainUUID)
	if err != nil {
		domainReadError(r, err)
		return
	}
	r.Response.Header().Set("Cache-Control", "no-store")
	response.Success(r, inspection)
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
		domainReadError(r, err)
		return
	}

	results, err := c.domainService.VerifyDNS(r.Context(), domain.ID)
	if err != nil {
		response.InternalError(r, "Unable to load domain data")
		return
	}

	// Reload domain to get updated status
	domain, err = c.domainService.GetDomain(r.Context(), claims.OrgID, domainUUID)
	if err != nil {
		domainReadError(r, err)
		return
	}
	records, err := c.domainService.GetDNSRecords(r.Context(), domain.ID)
	if err != nil {
		response.InternalError(r, "Unable to load domain DNS records")
		return
	}

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
		domainReadError(r, err)
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
		domainReadError(r, err)
		return
	}

	// Initiate SES verification
	sesRecords, err := c.domainService.InitiateSESVerification(r.Context(), domain.ID)
	if err != nil {
		response.BadRequest(r, domainOperationMessage(err))
		return
	}

	// Reload domain to get updated status
	domain, err = c.domainService.GetDomain(r.Context(), claims.OrgID, domainUUID)
	if err != nil {
		domainReadError(r, err)
		return
	}
	records, err := c.domainService.GetDNSRecords(r.Context(), domain.ID)
	if err != nil {
		response.InternalError(r, "Unable to load domain DNS records")
		return
	}

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
		domainReadError(r, err)
		return
	}

	status, err := c.domainService.CheckSESVerificationStatus(r.Context(), domain.ID)
	if err != nil {
		response.BadRequest(r, domainOperationMessage(err))
		return
	}

	// Reload domain to get updated status
	domain, err = c.domainService.GetDomain(r.Context(), claims.OrgID, domainUUID)
	if err != nil {
		domainReadError(r, err)
		return
	}

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
		domainReadError(r, err)
		return
	}

	// Add DNS records to Cloudflare
	results, err := c.domainService.AddDNSToCloudflare(r.Context(), domain.ID, req.APIToken, req.ZoneID)
	if err != nil {
		response.BadRequest(r, domainOperationMessage(err))
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
		response.BadRequest(r, domainOperationMessage(err))
		return
	}

	response.Success(r, zones)
}

// Keep missing resources distinguishable without returning driver errors or SQL
// details. Other domain operations retain plain validation errors only.
func domainReadError(r *ghttp.Request, err error) {
	if errors.Is(err, service.ErrDomainNotFound) {
		response.NotFound(r, "Domain not found")
		return
	}
	response.InternalError(r, "Unable to load domain data")
}

func domainOperationMessage(err error) string {
	if errors.Unwrap(err) != nil {
		return "Unable to complete domain operation"
	}
	return err.Error()
}
