package controllers

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/Wanjie-Ryan/clinic-booking-API/app/constants"
	"github.com/Wanjie-Ryan/clinic-booking-API/app/library"
	"github.com/Wanjie-Ryan/clinic-booking-API/app/models"
)

// availabilityCacheKey builds the cache key for a doctor's availability on a
// given date, in the clinic's local calendar (not UTC -- a booking near
// midnight in UTC can fall on a different local date, and it's the local date
// that GET /doctors/{id}/availability?date=... is keyed on).
func availabilityCacheKey(doctorID int64, date string) string {
	return fmt.Sprintf("availability:%d:%s", doctorID, date)
}

// idempotencyCacheKey builds the cache key for a POST /appointments
// Idempotency-Key header value.
func idempotencyCacheKey(key string) string {
	return fmt.Sprintf("idempotency:%s", key)
}

// cacheAvailability stores a computed availability response with the
// configured TTL. Called on every fresh computation (cache miss), including
// when the doctor has no working hours that day -- an empty result is still a
// valid, cacheable answer. Errors are logged and swallowed: a failed cache
// write should never turn a successful response into a failed request.
func (controller *Controller) cacheAvailability(ctx context.Context, cacheKey string, response models.AvailabilityResponse) {
	payload, err := json.Marshal(response)
	if err != nil {
		logrus.WithContext(ctx).WithFields(logrus.Fields{
			constants.DESCRIPTION: "error marshalling availability response for cache",
			constants.DATA:        cacheKey,
		}).Error(err.Error())
		return
	}

	if err := library.SetRedisKeyWithExpiry(ctx, controller.RedisConn, cacheKey, string(payload), controller.AvailabilityCacheTTL); err != nil {
		logrus.WithContext(ctx).WithFields(logrus.Fields{
			constants.DESCRIPTION: "error writing availability cache",
			constants.DATA:        cacheKey,
		}).Error(err.Error())
	}
}

// invalidateAvailabilityCache deletes the cached availability for a doctor on
// the local date a given UTC appointment time falls on. Called after a
// booking/cancel/reschedule write has already committed, never before (README
// section 1.5) -- if the delete itself fails, that's logged and swallowed, not
// returned as a request failure, since the write already succeeded and the
// cache will simply go stale until its TTL expires.
func (controller *Controller) invalidateAvailabilityCache(ctx context.Context, doctorID int64, startTimeUTC time.Time) {
	dateKey := startTimeUTC.In(controller.ClinicLocation).Format("2006-01-02")
	key := availabilityCacheKey(doctorID, dateKey)

	if err := library.DeleteRedisKey(ctx, controller.RedisConn, key); err != nil {
		logrus.WithContext(ctx).WithFields(logrus.Fields{
			constants.DESCRIPTION: "error invalidating availability cache",
			constants.DATA:        map[string]interface{}{"doctor_id": doctorID, "date": dateKey},
		}).Error(err.Error())
	}
}
