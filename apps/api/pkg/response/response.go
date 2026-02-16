package response

import (
	"github.com/gogf/gf/v2/net/ghttp"
)

type Response struct {
	Code    int         `json:"code"`
	Message string      `json:"message"`
	Data    interface{} `json:"data,omitempty"`
}

type PaginatedResponse struct {
	Data       interface{} `json:"data"`
	Pagination Pagination  `json:"pagination"`
}

type Pagination struct {
	Total   int `json:"total"`
	Page    int `json:"page"`
	PerPage int `json:"perPage"`
	Pages   int `json:"pages"`
}

// Success sends a successful JSON response
func Success(r *ghttp.Request, data interface{}) {
	r.Response.WriteJson(Response{
		Code:    0,
		Message: "success",
		Data:    data,
	})
}

// SuccessWithMessage sends a successful JSON response with a custom message
func SuccessWithMessage(r *ghttp.Request, message string, data interface{}) {
	r.Response.WriteJson(Response{
		Code:    0,
		Message: message,
		Data:    data,
	})
}

// Paginated sends a paginated JSON response
func Paginated(r *ghttp.Request, data interface{}, total, page, perPage int) {
	pages := total / perPage
	if total%perPage > 0 {
		pages++
	}

	r.Response.WriteJson(PaginatedResponse{
		Data: data,
		Pagination: Pagination{
			Total:   total,
			Page:    page,
			PerPage: perPage,
			Pages:   pages,
		},
	})
}

// Error sends an error JSON response
func Error(r *ghttp.Request, code int, message string) {
	r.Response.WriteJsonExit(Response{
		Code:    code,
		Message: message,
	})
}

// BadRequest sends a 400 error response
func BadRequest(r *ghttp.Request, message string) {
	r.Response.Status = 400
	Error(r, 400, message)
}

// Unauthorized sends a 401 error response
func Unauthorized(r *ghttp.Request, message string) {
	r.Response.Status = 401
	Error(r, 401, message)
}

// Forbidden sends a 403 error response
func Forbidden(r *ghttp.Request, message string) {
	r.Response.Status = 403
	Error(r, 403, message)
}

// NotFound sends a 404 error response
func NotFound(r *ghttp.Request, message string) {
	r.Response.Status = 404
	Error(r, 404, message)
}

// InternalError sends a 500 error response
func InternalError(r *ghttp.Request, message string) {
	r.Response.Status = 500
	Error(r, 500, message)
}

// Created sends a 201 created response
func Created(r *ghttp.Request, data interface{}) {
	r.Response.Status = 201
	r.Response.WriteJson(Response{
		Code:    0,
		Message: "created",
		Data:    data,
	})
}
