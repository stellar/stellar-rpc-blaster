package benchmark

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	vegeta "github.com/tsenart/vegeta/v12/lib"

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

func recordPollRequestTrace(workload string, hash string, rpcID int64, attempt int, recorder *benchmarkTraceRecorder) {
	if recorder == nil {
		return
	}
	recorder.record(benchmarkTraceRecord{
		Timestamp:   time.Now(),
		Workload:    workload,
		Event:       "poll_request",
		RPCID:       rpcID,
		Method:      protocol.GetTransactionMethodName,
		Hash:        hash,
		Attempt:     attempt,
		RequestBody: fmt.Sprintf(`{"hash":%q}`, hash),
	})
}

func recordPollResponseTrace(workload string, hash string, rpcID int64, attempt int, resp *protocol.GetTransactionResponse, err error, recorder *benchmarkTraceRecorder) {
	if recorder == nil {
		return
	}
	record := benchmarkTraceRecord{
		Timestamp: time.Now(),
		Workload:  workload,
		Event:     "poll_response",
		RPCID:     rpcID,
		Method:    protocol.GetTransactionMethodName,
		Hash:      hash,
		Attempt:   attempt,
	}
	if err != nil {
		record.Error = err.Error()
		recorder.record(record)
		return
	}
	if resp != nil {
		record.TransactionStatus = resp.Status
		if resp.Status == protocol.TransactionStatusFailed {
			record.ResultCode = ledger.DecodeTransactionResultCode(resp.ResultXDR)
		}
		responseBody, marshalErr := json.Marshal(resp)
		if marshalErr != nil {
			record.Error = fmt.Sprintf("marshal poll response: %v", marshalErr)
		} else {
			record.ResponseBody = string(responseBody)
		}
	}
	recorder.record(record)
}
