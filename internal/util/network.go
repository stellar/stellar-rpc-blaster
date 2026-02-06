package util

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"

	"github.com/pkg/errors"
)

// FetchNetworkPassphrase makes a getNetwork RPC call to fetch the network passphrase
func FetchResponseField(rpcURL string, endpoint string, field string, params map[string]any) (any, error) {
	reqBody := map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  endpoint,
		"params":  params,
	}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to marshal %s request", endpoint)
	}

	resp, err := http.Post(rpcURL, "application/json", bytes.NewBuffer(jsonData))
	if err != nil {
		return nil, errors.Wrapf(err, "failed to make %s request", endpoint)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to read %s response", endpoint)
	}

	var result struct {
		Result map[string]any `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}

	if err := json.Unmarshal(body, &result); err != nil {
		return nil, errors.Wrapf(err, "failed to parse %s response", endpoint)
	}

	if result.Error != nil {
		return nil, errors.Errorf("%s RPC error: %s (code %d)", endpoint, result.Error.Message, result.Error.Code)
	}

	value, ok := result.Result[field]
	if !ok {
		return nil, errors.Errorf("%s response missing field '%s'", endpoint, field)
	}

	return value, nil
}
