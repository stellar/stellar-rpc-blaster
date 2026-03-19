package engine

import "encoding/json"

type jsonRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type jsonRPCResponse struct {
	Error *jsonRPCError `json:"error,omitempty"`
}

// parseRPCError returns the RPC error if the body contains one, nil otherwise.
func parseRPCError(body []byte) *jsonRPCError {
	if len(body) == 0 {
		return nil
	}
	var resp jsonRPCResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil
	}
	return resp.Error
}
