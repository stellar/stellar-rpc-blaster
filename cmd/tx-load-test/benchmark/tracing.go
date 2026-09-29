package benchmark

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	vegeta "github.com/tsenart/vegeta/v12/lib"

	"github.com/stellar/go-stellar-sdk/clients/rpcclient"
	protocol "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/ledger"
)

type benchmarkTraceRecorder struct {
	mu   sync.Mutex
	file *os.File
	enc  *json.Encoder
}

type benchmarkTraceRecord struct {
	Timestamp         time.Time `json:"timestamp"`
	Workload          string    `json:"workload"`
	Event             string    `json:"event"`
	RPCID             int64     `json:"rpc_id,omitempty"`
	Method            string    `json:"method,omitempty"`
	URL               string    `json:"url,omitempty"`
	Hash              string    `json:"hash,omitempty"`
	Attempt           int       `json:"attempt,omitempty"`
	HTTPStatus        int       `json:"http_status,omitempty"`
	TransactionStatus string    `json:"transaction_status,omitempty"`
	ResultCode        string    `json:"result_code,omitempty"`
	Error             string    `json:"error,omitempty"`
	RequestBody       string    `json:"request_body,omitempty"`
	ResponseBody      string    `json:"response_body,omitempty"`
}

func openBenchmarkTraceRecorder(path string) (*benchmarkTraceRecorder, error) {
	if path == "" {
		return nil, nil
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create trace directory %q: %w", dir, err)
		}
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open trace file %q: %w", path, err)
	}
	enc := json.NewEncoder(file)
	enc.SetEscapeHTML(false)
	return &benchmarkTraceRecorder{file: file, enc: enc}, nil
}

func (r *benchmarkTraceRecorder) Close() error {
	if r == nil || r.file == nil {
		return nil
	}
	return r.file.Close()
}

func (r *benchmarkTraceRecorder) record(record benchmarkTraceRecord) {
	if r == nil || r.enc == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	_ = r.enc.Encode(record)
}

func traceTargeter(workload string, base vegeta.Targeter, recorder *benchmarkTraceRecorder) vegeta.Targeter {
	if recorder == nil {
		return base
	}
	return func(target *vegeta.Target) error {
		if err := base(target); err != nil {
			recorder.record(benchmarkTraceRecord{
				Timestamp: time.Now(),
				Workload:  workload,
				Event:     "submit_build_error",
				Error:     err.Error(),
			})
			return err
		}

		record := benchmarkTraceRecord{
			Timestamp:   time.Now(),
			Workload:    workload,
			Event:       "submit_request",
			URL:         target.URL,
			RequestBody: string(target.Body),
		}
		var request rpcJSONBody
		if err := json.Unmarshal(target.Body, &request); err == nil {
			record.RPCID = request.ID
			record.Method = request.Method
		}
		recorder.record(record)
		return nil
	}
}

func pollTransactionWithTrace(
	ctx context.Context,
	rpc *rpcclient.Client,
	hash string,
	workload string,
	recorder *benchmarkTraceRecorder,
) (protocol.GetTransactionResponse, error) {
	interval := 500 * time.Millisecond
	attempt := 0
	for {
		attempt++
		if recorder != nil {
			recorder.record(benchmarkTraceRecord{
				Timestamp: time.Now(),
				Workload:  workload,
				Event:     "poll_request",
				Method:    protocol.GetTransactionMethodName,
				Hash:      hash,
				Attempt:   attempt,
				RequestBody: fmt.Sprintf(`{"hash":%q}`,
					hash),
			})
		}

		resp, err := rpc.GetTransaction(ctx, protocol.GetTransactionRequest{Hash: hash})
		if err != nil {
			if recorder != nil {
				recorder.record(benchmarkTraceRecord{
					Timestamp: time.Now(),
					Workload:  workload,
					Event:     "poll_response",
					Method:    protocol.GetTransactionMethodName,
					Hash:      hash,
					Attempt:   attempt,
					Error:     err.Error(),
				})
			}
			return protocol.GetTransactionResponse{}, err
		}

		resultCode := ""
		if resp.Status == protocol.TransactionStatusFailed {
			resultCode = ledger.DecodeTransactionResultCode(resp.ResultXDR)
		}
		if recorder != nil {
			responseBody, marshalErr := json.Marshal(resp)
			record := benchmarkTraceRecord{
				Timestamp:         time.Now(),
				Workload:          workload,
				Event:             "poll_response",
				Method:            protocol.GetTransactionMethodName,
				Hash:              hash,
				Attempt:           attempt,
				TransactionStatus: resp.Status,
				ResultCode:        resultCode,
			}
			if marshalErr != nil {
				record.Error = fmt.Sprintf("marshal poll response: %v", marshalErr)
			} else {
				record.ResponseBody = string(responseBody)
			}
			recorder.record(record)
		}

		switch resp.Status {
		case protocol.TransactionStatusSuccess, protocol.TransactionStatusFailed:
			return resp, nil
		}

		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return protocol.GetTransactionResponse{}, ctx.Err()
		case <-timer.C:
		}
		interval = min(interval*2, 3500*time.Millisecond)
	}
}
