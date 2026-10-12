package controllers

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"    // co-ordinates multiple concurrent goroutines
	"testing" // testing package
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

// UNIT TESTS - one small piece of logic in isolation - no real DB, network call, nothing external. like the slots and validation tests.
// if the function needs a DB, you hand it a fake one. The point is speed and isolation; you're testing logic, not anyone else's infra.

// INTEGRATION TEST - Tests how your code behaves when its actually wired up to a real external systems - MySQL, real redis.

// The whole point of TestBookAppointment_ConcurrentBookingsOnlyOneSucceeds is proving that MySQL's real unique constraint on active_slot_key actually rejects a duplicate insert.
// If you swapped in a fake/mocked DB for this test, the fake would just do whatever mock code you told it to do - it wouldn't have a real active_slot_key column, a real unique index, or real innoDB behaviour underneath it.
// Thats why newTestController refuses to start unless a real DATABASE_HOST is configured.

// COMMAND - go test

// t *testing.T - this is the toolkit inside a test - its how you report failures, skip tests, log things.
// t.Errorf - mark the test as failed, but keep running the rest of the function.
// t.Fatal/t.FailNow - mark as failed and stop immediately, don't run anything after this line.
// t.Skip - don't fail, don't pass, just skip this test entirely
// t.helper - bookkeeping call.

// httptest - The handlers (BookAppointment, CancelAppointment) are ordinary Go functions with the signature func (echo.Context) error - they don't require a running server listening on a port to be called.
// httptest.NewRequest - builds a fake, in-memory HTTP request object with 0 actual networking involved.
// httptest.NewRecorder - builds a fake response writer that just captures whatever status code and body your handler writes to it, so you can inspect it afterward.
// e.NewContext(req, rec) - wraps both into the echo.Context object your handler expects.
// you can then call the handler function directly, like any other GO function call - controller.BookAppointment(c) - no server, no port, no real HTTP round trip.
// this is the standard way to test HTTP handlers in GO without spinning the whole app.

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

// it gives a date AT LEAST ONE WEEK AWAY at exactly 9:00 AM nairobi time.
// the function exists purely so that the test always books a slot safely in the future, no matter what day you happen to run go test on.
func nextWeekdayAt(day time.Weekday, hour, minute int) time.Time {
	// loads the Nairobi timezone
	loc, _ := time.LoadLocation("Africa/Nairobi")
	// gets the time.now, like right now, whats the time?
	now := time.Now().In(loc)
	// calculates how many days until monday, Sunday being 0, Saturday being 6
	// assume now is saturday which is 6 and the time.Monday has been passes which is 1
	// daysUntil = (1-6+7)%7
	daysUntil := (int(day) - int(now.Weekday()) + 7) % 7
	if daysUntil < 2 {
		daysUntil += 7
		// if the natural answer is less than 2 days away, push it out a full extra week instead.
	}
	target := now.AddDate(0, 0, daysUntil)
	return time.Date(target.Year(), target.Month(), target.Day(), hour, minute, 0, 0, loc)
	// returns something like 2026-09-14 09:00:00 +03:00
}

// TestBookAppointment_ConcurrentBookingsOnlyOneSucceeds is the test the
// assessment explicitly asks for: race N goroutines at the same doctor+slot and
// assert exactly one gets 201 and the rest get 409, proving the database
// constraint -- not just the application's courtesy check -- is what prevents
// double-booking under real concurrency.
func TestBookAppointment_ConcurrentBookingsOnlyOneSucceeds(t *testing.T) {
	controller := newTestController(t)
	e := echo.New()

	// the function nextWeekdayAt requires 3 inputs weekday you want (time.Monday), what hour 9, what minute 0, and the function returns one time.Time.
	start := nextWeekdayAt(time.Monday, 9, 0)
	body := fmt.Sprintf(`{"doctor_id":1,"patient_id":1,"start_time":%q}`, start.Format(time.RFC3339))
	// {"doctor_id":1,"patient_id":1,"start_time":"2026-09-14T09:00:00+03:00"}
	// returns something like 2026-09-14 09:00:00 +03:00
	// this is the JSON body reused over the 10 attempts, which all try to claim that one spot

	const attempts = 10               // fire 10 near simultaneous booking requests
	statuses := make([]int, attempts) // creates an int slice with 10 slots, each pre-filled with 0. statues[0], statuses[0]
	// the reason for creating this makeshift array is cause we are running 10 concurrent requests, and each request will get back its own HTTP status code, either 201 or 409, wee need somewhere to record this goroutine got status X, for every 10 of the requests, so that after we can go back and count how many got 201 VS 409
	var wg sync.WaitGroup
	// how many background jobs are still running.

	for i := 0; i < attempts; i++ {
		wg.Add(1) // incremeber the counter by one
		// launch 10 separate go routines all trying to make this one book request
		go func(i int) {
			defer wg.Done() // guarantees the counter decrements when this goroutine finishes, even if sth inside panics. defer means run this line right before the function returns, no matter how it returns
			req := httptest.NewRequest(http.MethodPost, "/appointments", strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			// httptest.NewRecorder - builds a fake response writer that just captures whatever status code and body your handler writes to it, so you can inspect it afterward.
			c := e.NewContext(req, rec)
			_ = controller.BookAppointment(c)
			statuses[i] = rec.Code
			// each goroutine independently builds its own fake request (same body JSON) its own fake reponse recorder, wraps them into an echo.Context, then calls controller.BookAppointment directly.
			// the _ diregards the error return value, since we only care about the HTTP status code it wrote into rec, not the Go-level error
		}(i)
	}
	wg.Wait() // blocks, pauses until counter hits 0, until all 10 goroutines have called wg.Done()
	// tha main test function, the goRoutines running the executions, it pauses here and does nothing until the shared counter which started at 10 decrements back to 0, meaning every single one of the 10 goroutines has finished and called wg.Done()
	// without this line, the test would immediately race ahead to check statuses while some or all of the 10 booking attempts might still be mid-flight hence incorrect results

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
	// require stops the test immediately on failure
	require.Equal(t, attempts-1, conflictCount, "every other concurrent attempt must be rejected with 409")
	// actual proof is here, MySQL's constraint is working, exactly 1 of the 10 got 201 and the other 9 got 409

	_, _ = controller.DB.Exec("DELETE FROM appointments WHERE doctor_id = 1 AND start_time = ?", start.UTC())
	// deletes the test on the DB
}

// TestBookAppointment_RejectsOutsideWorkingHours checks the controller-level
// wiring of library.ValidateSlot, not just the library function itself (which
// already has its own unit tests in app/library).

// 8:00 - 12:30 
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

	// The Go function call itself didn't return a Go level error.
	require.NoError(t, controller.BookAppointment(c))

	require.Equal(t, http.StatusBadRequest, rec.Code)

	// the test isn't checking ValidateSlot logic itself, thats already unit tested separately. This function test that the controller correctly wires ValidateSlot into the HTTP layer - that when the function returns slotoutsideworkinghours, the controller translates that into real 400 response.
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

	// the ID is being handed to the CancelAppointment directly, we are sidestepping the endpoint way, of declaring it fully, pass the ID directly into cancelAppointment.
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
