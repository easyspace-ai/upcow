package server

import "net/http"

func chiURLParam(r *http.Request, key string) string {
	if r == nil {
		return ""
	}
	v := r.Context().Value(paramsKey)
	if v == nil {
		return ""
	}
	m, ok := v.(map[string]string)
	if !ok {
		return ""
	}
	return m[key]
}
