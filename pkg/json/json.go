package json

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/rs/zerolog/log"
)

type apiError struct {
	Message string `json:"error"`
}

// JSONInternalServerError handles a JSON InternalServerError.
func JSONInternalServerError(rw http.ResponseWriter, errMsg string, args ...interface{}) {
	JSONError(rw, http.StatusInternalServerError, fmt.Sprintf(errMsg, args...))
}

// JSONErrorf handles a JSON error.
func JSONErrorf(rw http.ResponseWriter, code int, errMsg string, args ...interface{}) {
	JSONError(rw, code, fmt.Sprintf(errMsg, args...))
}

// JSONError handles a JSON error.
func JSONError(rw http.ResponseWriter, code int, errMsg string) {
	rw.Header().Set("Content-Type", "application/json")
	rw.Header().Set("X-Content-Type-Options", "nosniff")
	rw.WriteHeader(code)

	msg := apiError{
		Message: errMsg,
	}

	content, err := json.Marshal(msg)
	if err != nil {
		log.Error().Err(err).Str("initial_error", errMsg).Msg("Unable to encode JSON error")

		_, _ = rw.Write([]byte(`{"error": "Internal Server Error"}`))
		return
	}

	_, _ = rw.Write(content)
}

// JSON renders JSON response for the given object and status code in the ResponseWriter.
func JSON(rw http.ResponseWriter, statusCode int, v interface{}) error {
	rw.Header().Set("Content-Type", "application/json")
	rw.WriteHeader(statusCode)

	if v == nil {
		return nil
	}

	return json.NewEncoder(rw).Encode(v)
}
