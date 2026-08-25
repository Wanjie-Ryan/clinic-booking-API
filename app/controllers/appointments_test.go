package controllers

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"

	"github.com/Wanjie-Ryan/clinic-booking-API/app/database"
)

// These are integration tests, not unit tests with mocks: the invariant under
// test in TestBookAppointment_ConcurrentBookingsOnlyOneSucceeds is enforced by
// MySQL's unique constraint on active_slot_key (README section 1.3), which
// cannot be faked with a mock database -- the whole point is proving the real
// constraint does the job. They need a live MySQL and Redis, exactly like
// local dev (docker compose up) and CI (service containers) both provide.

func newTestController(t *testing.T) *Controller {
	t.Helper()

	if os.Getenv("DATABASE_HOST") == "" {
		t.Skip("DATABASE_HOST not set -- these are integration tests, run them with the same env as `go run .` (see README section 2)")
	}

	loc, err := time.LoadLocation("Africa/Nairobi")
	require.NoError(t, err)

	return &Controller{
		DB:                   database.DbInstance(),
		RedisConn:            database.RedisClient(),
		Tracer:               otel.Tracer("test"),
		ClinicLocation:       loc,
		SlotDuration:         30 * time.Minute,
		MinimumLeadTime:      60 * time.Minute,
		AvailabilityCacheTTL: 60 * time.Second,
		IdempotencyKeyTTL:    24 * time.Hour,
	}
}

// nextWeekdayAt returns the next occurrence of the given weekday at hour:minute
// Nairobi time, always at least one full week out so it can never accidentally
// land inside the minimum-lead-time window.
func nextWeekdayAt(day time.Weekday, hour, minute int) time.Time {
	loc, _ := time.LoadLocation("Africa/Nairobi")
	now := time.Now().In(loc)
	daysUntil := (int(day) - int(now.Weekday()) + 7) % 7
	if daysUntil < 2 {
		daysUntil += 7
	}
	target := now.AddDate(0, 0, daysUntil)
	return time.Date(target.Year(), target.Month(), target.Day(), hour, minute, 0, 0, loc)
}

// TestBookAppointment_ConcurrentBookingsOnlyOneSucceeds is the test the
// assessment explicitly asks for: race N goroutines at the same doctor+slot and
// assert exactly one gets 201 and the rest get 409, proving the database
// constraint -- not just the application's courtesy check -- is what prevents
// double-booking under real concurrency.
func TestBookAppointment_ConcurrentBookingsOnlyOneSucceeds(t *testing.T) {
	controller := newTestController(t)
	e := echo.New()

	start := nextWeekdayAt(time.Monday, 9, 0)
	body := fmt.Sprintf(`{"doctor_id":1,"patient_id":1,"start_time":%q}`, start.Format(time.RFC3339))

	const attempts = 10
	statuses := make([]int, attempts)
	var wg sync.WaitGroup

	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			req := httptest.NewRequest(http.MethodPost, "/appointments", strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			c := e.NewContext(req, rec)
			_ = controller.BookAppointment(c)
			statuses[i] = rec.Code
		}(i)
	}
	wg.Wait()

	successCount, conflictCount := 0, 0
	for _, code := range statuses {
		switch code {
		case http.StatusCreated:
			successCount++
		case http.StatusConflict:
			conflictCount++
		default:
			t.Errorf("unexpected status code %d", code)
		}
	}

	require.Equal(t, 1, successCount, "exactly one concurrent booking attempt must succeed")
	require.Equal(t, attempts-1, conflictCount, "every other concurrent attempt must be rejected with 409")

	_, _ = controller.DB.Exec("DELETE FROM appointments WHERE doctor_id = 1 AND start_time = ?", start.UTC())
}

// TestBookAppointment_RejectsOutsideWorkingHours checks the controller-level
// wiring of library.ValidateSlot, not just the library function itself (which
// already has its own unit tests in app/library).
func TestBookAppointment_RejectsOutsideWorkingHours(t *testing.T) {
	controller := newTestController(t)
	e := echo.New()

	// Doctor 1 works 08:00-13:00 and 14:00-17:00 -- 13:30 falls in the lunch
	// gap between the two ranges.
	start := nextWeekdayAt(time.Tuesday, 13, 30)
	body := fmt.Sprintf(`{"doctor_id":1,"patient_id":1,"start_time":%q}`, start.Format(time.RFC3339))

	req := httptest.NewRequest(http.MethodPost, "/appointments", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	require.NoError(t, controller.BookAppointment(c))
	require.Equal(t, http.StatusBadRequest, rec.Code)
}

// TestBookAppointment_UnknownDoctorReturns404 checks the existence-check path,
// which the pure library layer has no way to test since it doesn't touch the
// database.
func TestBookAppointment_UnknownDoctorReturns404(t *testing.T) {
	controller := newTestController(t)
	e := echo.New()

	start := nextWeekdayAt(time.Wednesday, 9, 0)
	body := fmt.Sprintf(`{"doctor_id":999999,"patient_id":1,"start_time":%q}`, start.Format(time.RFC3339))

	req := httptest.NewRequest(http.MethodPost, "/appointments", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	require.NoError(t, controller.BookAppointment(c))
	require.Equal(t, http.StatusNotFound, rec.Code)
}

// TestCancelAppointment_TwiceReturnsConflict exercises the WHERE status =
// 'booked' guard in the UPDATE statement -- the mechanism that makes
// cancelling an already-cancelled appointment fail cleanly instead of silently
// re-applying.
func TestCancelAppointment_TwiceReturnsConflict(t *testing.T) {
	controller := newTestController(t)
	e := echo.New()

	start := nextWeekdayAt(time.Thursday, 9, 0)
	bookBody := fmt.Sprintf(`{"doctor_id":1,"patient_id":1,"start_time":%q}`, start.Format(time.RFC3339))

	bookReq := httptest.NewRequest(http.MethodPost, "/appointments", strings.NewReader(bookBody))
	bookReq.Header.Set("Content-Type", "application/json")
	bookRec := httptest.NewRecorder()
	bookCtx := e.NewContext(bookReq, bookRec)
	require.NoError(t, controller.BookAppointment(bookCtx))
	require.Equal(t, http.StatusCreated, bookRec.Code)

	var appointmentID int64
	require.NoError(t, controller.DB.QueryRow(
		"SELECT id FROM appointments WHERE doctor_id = 1 AND start_time = ?", start.UTC(),
	).Scan(&appointmentID))

	cancelOnce := func() int {
		req := httptest.NewRequest(http.MethodPatch, "/", strings.NewReader(`{"reason":"test"}`))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)
		c.SetParamNames("id")
		c.SetParamValues(fmt.Sprintf("%d", appointmentID))
		_ = controller.CancelAppointment(c)
		return rec.Code
	}

	require.Equal(t, http.StatusOK, cancelOnce(), "first cancel should succeed")
	require.Equal(t, http.StatusConflict, cancelOnce(), "second cancel should be rejected")

	_, _ = controller.DB.Exec("DELETE FROM appointments WHERE id = ?", appointmentID)
}
