package githubapp

// githubAppError is the shared base carrier for this package's typed errors
// (a Go idiom for the TS Error-subclass hierarchy). Concrete errors embed
// *githubAppError and are matched with errors.As in tests/callers.
type githubAppError struct {
	message string
}

func newGithubAppError(message string) *githubAppError { return &githubAppError{message: message} }

// Error implements error (the TS `Error.message`, preserved verbatim).
func (e *githubAppError) Error() string { return e.message }

// Message reports the observable TS message text (TS `error.message`).
func (e *githubAppError) Message() string { return e.message }
