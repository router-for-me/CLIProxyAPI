package httpwire

import (
	"net/http"
	"reflect"
)

// FlushResponse prefers error-aware flushing and preserves legacy middleware.
// Only Gin's raw void Flush is bypassed so transport errors remain observable.
func FlushResponse(w http.ResponseWriter) error {
	for {
		if flusher, ok := w.(interface{ FlushError() error }); ok {
			return flusher.FlushError()
		}
		if flusher, ok := w.(http.Flusher); ok {
			if !isGinResponseWriter(w) {
				flusher.Flush()
				return nil
			}
			// Match Gin's Flush header accounting before reaching its transport.
			w.(interface{ WriteHeaderNow() }).WriteHeaderNow()
		}
		unwrapper, ok := w.(interface{ Unwrap() http.ResponseWriter })
		if !ok {
			return http.NewResponseController(w).Flush()
		}
		w = unwrapper.Unwrap()
	}
}

func isGinResponseWriter(w http.ResponseWriter) bool {
	// Gin's concrete writer is unexported. Interface checks also match wrappers
	// embedding gin.ResponseWriter, whose Flush may drain buffers or compressors.
	// Restrict this exception to the exact package/type; never inspect its fields.
	t := reflect.TypeOf(w)
	return t != nil && t.Kind() == reflect.Pointer &&
		t.Elem().PkgPath() == "github.com/gin-gonic/gin" && t.Elem().Name() == "responseWriter"
}
