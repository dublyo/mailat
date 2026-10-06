package model

import "time"

type SignupForm struct {
	ID               int64     `json:"-"`
	UUID             string    `json:"uuid"`
	OrgID            int64     `json:"-"`
	ListID           int       `json:"-"`
	ListUUID         string    `json:"listId"`
	ListName         string    `json:"listName"`
	CreatedBy        int64     `json:"-"`
	IdentityID       string    `json:"identityId"`
	FromEmail        string    `json:"fromEmail"`
	Name             string    `json:"name"`
	Title            string    `json:"title"`
	Description      string    `json:"description"`
	ConsentText      string    `json:"consentText"`
	ButtonText       string    `json:"buttonText"`
	PrivacyURL       string    `json:"privacyUrl"`
	CollectName      bool      `json:"collectName"`
	Published        bool      `json:"published"`
	Version          int       `json:"version"`
	ConfirmationMode string    `json:"confirmationMode"`
	Subscribed       int       `json:"subscribed"`
	Pending          int       `json:"pending"`
	CreatedAt        time.Time `json:"createdAt"`
}
type SaveSignupFormRequest struct {
	ListID      string `json:"listId"`
	IdentityID  string `json:"identityId"`
	Name        string `json:"name"`
	Title       string `json:"title"`
	Description string `json:"description"`
	ConsentText string `json:"consentText"`
	ButtonText  string `json:"buttonText"`
	PrivacyURL  string `json:"privacyUrl"`
	CollectName bool   `json:"collectName"`
	Published   bool   `json:"published"`
}
type PublicSignupForm struct {
	UUID             string `json:"uuid"`
	Title            string `json:"title"`
	Description      string `json:"description"`
	ConsentText      string `json:"consentText"`
	ButtonText       string `json:"buttonText"`
	PrivacyURL       string `json:"privacyUrl"`
	CollectName      bool   `json:"collectName"`
	ConfirmationMode string `json:"confirmationMode"`
	Challenge        string `json:"challenge"`
}
type SubmitSignupRequest struct {
	Email     string `json:"email"`
	FirstName string `json:"firstName"`
	Consent   bool   `json:"consent"`
	Challenge string `json:"challenge"`
	Website   string `json:"website"` // Honeypot; real visitors leave this empty.
}
type SignupResult struct {
	Message string `json:"message"`
}
type ConfirmSignupRequest struct {
	Token string `json:"token"`
}
type SignupEntry struct {
	ID               string     `json:"id"`
	Email            string     `json:"email"`
	FirstName        string     `json:"firstName"`
	Status           string     `json:"status"`
	ConfirmationMode string     `json:"confirmationMode"`
	CreatedAt        time.Time  `json:"createdAt"`
	ConfirmedAt      *time.Time `json:"confirmedAt"`
}
type SignupEntries struct {
	Items    []SignupEntry `json:"items"`
	Total    int           `json:"total"`
	Page     int           `json:"page"`
	PageSize int           `json:"pageSize"`
}
