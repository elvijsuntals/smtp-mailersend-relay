package dispatch

import (
	"context"
	"time"

	"golang.org/x/time/rate"
)

type RequestLimiter interface {
	Wait(ctx context.Context) error
}

type noopLimiter struct{}

func (noopLimiter) Wait(context.Context) error {
	return nil
}

func NewRequestLimiter(requestsPerMin int) RequestLimiter {
	if requestsPerMin <= 0 {
		return noopLimiter{}
	}
	return rate.NewLimiter(rate.Every(time.Minute/time.Duration(requestsPerMin)), 1)
}
