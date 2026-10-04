package service

import (
	"context"
	"github.com/dublyo/mailat/api/internal/model"
)

func (s *OAuthService) AuthenticateLogin(ctx context.Context, userID int64) (*model.LoginResponse, error) {
	return NewAuthService(s.db, s.cfg).AuthenticateUser(ctx, userID)
}
