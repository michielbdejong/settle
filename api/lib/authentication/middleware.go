package authentication

import (
	"net/http"
	"regexp"

	"github.com/spolu/settle/lib/errors"
	"github.com/spolu/settle/lib/livemode"
	"github.com/spolu/settle/lib/logging"
	"github.com/spolu/settle/lib/respond"
	"github.com/spolu/settle/model"

	"goji.io"

	"golang.org/x/net/context"
)

const (
	// statusKey the context.Context key to store the authentication status.
	statusKey string = "authentication.status"
)

// AutStatus indicates the status of the authentication.
type AutStatus string

const (
	// AutStSucceeded indicates a successful authentication.
	AutStSucceeded AutStatus = "succeeded"
	// AutStSkipped indicates a skipped authentication.
	AutStSkipped AutStatus = "skipped"
	// AutStFailed indicates a failed authentication.
	AutStFailed AutStatus = "failed"
)

// Status stores the authentication information.
type Status struct {
	Status  AutStatus
	Address string
}

// With stores the authentication information in a new context.
func With(
	ctx context.Context,
	status Status,
) context.Context {
	return context.WithValue(ctx, statusKey, status)
}

// Get retrieves the authenticaiton information form the context.
func Get(
	ctx context.Context,
) Status {
	return ctx.Value(statusKey).(Status)
}

// SkipRule defines a skip rule for authentication
type SkipRule struct {
	Method  string
	Pattern *regexp.Regexp
}

// SkipList is the list of endpoints that do not require authentication.
var SkipList = []*SkipRule{
	&SkipRule{"GET", regexp.MustCompile("^/challenges$")},
	&SkipRule{"GET", regexp.MustCompile("^/users/[a-zA-Z0-9_]+$")},
}

type middleware struct {
	goji.Handler
}

// ServeHTTPC handles incoming HTTP requests and attempt to authenticate them.
func (m middleware) ServeHTTPC(
	ctx context.Context,
	w http.ResponseWriter,
	r *http.Request,
) {
	withStatus := With(ctx, Status{AutStFailed, ""})

	address, signature, _ := r.BasicAuth()
	challenge := r.Header.Get("Authorization-Challenge")
	skip := false
	for _, s := range SkipList {
		if s.Method == r.Method && s.Pattern.MatchString(r.URL.EscapedPath()) {
			skip = true
		}
	}

	// Helper closure to fallback to the skiplist or log and return an
	// authentication error.
	failedAuth := func(err error) {
		if skip {
			withStatus = With(ctx, Status{AutStSkipped, ""})
			logging.Logf(ctx, "Authentication: status=%q livemode=%t",
				Get(withStatus).Status, livemode.Get(ctx))

			m.Handler.ServeHTTPC(withStatus, w, r)
		} else {
			withStatus = With(ctx, Status{AutStFailed, ""})
			logging.Logf(ctx,
				"Authentication: status=%q livemode=%t address=%q "+
					"challenge=%q signature=%q",
				Get(withStatus).Status, livemode.Get(ctx),
				address, challenge, signature)

			respond.Error(withStatus, w, errors.Trace(err))
		}
	}

	// Check that the challenge is valid.
	err := CheckChallenge(ctx, challenge, RootLiveKeypair)
	if err != nil {
		failedAuth(errors.Trace(err))
		return
	}

	// Verify the challenge signature passed as basic auth.
	err = VerifyChallenge(ctx, challenge, address, signature)
	if err != nil {
		failedAuth(errors.Trace(err))
		return
	}

	// Check that the challenge was never used.
	auth, err := model.LoadAuthenticationByChallenge(ctx, challenge)
	if err != nil {
		failedAuth(errors.Trace(err))
		return
	} else if auth != nil {
		failedAuth(errors.NewUserError(err,
			400, "challenge_already_used",
			"The challenge you provided was already used. You must "+
				"resolve a new challenge for each API request.",
		))
		return
	}

	auth, err = model.CreateAuthentication(ctx,
		r.Method, r.URL.String(), challenge, address, signature)
	if err != nil {
		failedAuth(errors.Trace(err))
		return
	}

	withStatus = With(ctx, Status{AutStSucceeded, address})
	logging.Logf(ctx,
		"Authentication: status=%q livemode=%t address=%q "+
			"challenge=%q signature=%q",
		Get(withStatus).Status, livemode.Get(ctx),
		address, challenge, signature)

	m.Handler.ServeHTTPC(withStatus, w, r)
}

// Middleware that authenticates API requests.
func Middleware(h goji.Handler) goji.Handler {
	return middleware{h}
}