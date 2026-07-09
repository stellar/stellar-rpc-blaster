package benchmark

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	vegeta "github.com/tsenart/vegeta/v12/lib"

	"github.com/stellar/go-stellar-sdk/clients/rpcclient"
	protocol "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stretchr/testify/require"
)

type rpcTraceEnvelope struct {
	JSONRPC string `json:"jsonrpc"`
	ID      any    `json:"id"`
	Result  any    `json:"result"`
}

func readTraceRecords(t *testing.T, path string) []benchmarkTraceRecord {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	if len(data) == 0 {
		return nil
	}
	lines := bytes.Split(bytes.TrimSpace(data), []byte("\n"))
	records := make([]benchmarkTraceRecord, 0, len(lines))
	for _, line := range lines {
		var record benchmarkTraceRecord
		require.NoError(t, json.Unmarshal(line, &record))
		records = append(records, record)
	}
	return records
}

func TestTraceTargeterWritesSubmitRequest(t *testing.T) {
	traceFile := t.TempDir() + "/bench-trace.ndjson"
	recorder, err := openBenchmarkTraceRecorder(traceFile)
	require.NoError(t, err)

	targeter := traceTargeter("simple-payment", func(target *vegeta.Target) error {
		body, err := json.Marshal(rpcJSONBody{
			JSONRPC: "2.0",
			ID:      42,
			Method:  protocol.SendTransactionMethodName,
			Params:  map[string]string{"transaction": "AAAA"},
		})
		if err != nil {
			return err
		}
		populateJSONRPCTarget(target, "https://rpc.example", body, 0)
		return nil
	}, recorder)

	var target vegeta.Target
	require.NoError(t, targeter(&target))
	require.NoError(t, recorder.Close())

	records := readTraceRecords(t, traceFile)
	require.Len(t, records, 1)
	require.Equal(t, "simple-payment", records[0].Workload)
	require.Equal(t, "submit_request", records[0].Event)
	require.Equal(t, int64(42), records[0].RPCID)
	require.Equal(t, protocol.SendTransactionMethodName, records[0].Method)
	require.Equal(t, "https://rpc.example", records[0].URL)
	require.Contains(t, records[0].RequestBody, "transaction")
}

func TestProcessAttackResultWritesSubmitResponseTrace(t *testing.T) {
	traceFile := t.TempDir() + "/bench-trace.ndjson"
	recorder, err := openBenchmarkTraceRecorder(traceFile)
	require.NoError(t, err)

	responseBody, err := json.Marshal(sendRespEnvelope{
		ID: 9,
		Result: protocol.SendTransactionResponse{
			Status: "PENDING",
			Hash:   "abc123",
		},
	})
	require.NoError(t, err)

	metrics := vegeta.Metrics{}
	state := newAttackState(1)
	processAttackResult(&vegeta.Result{Code: 200, Timestamp: time.Unix(10, 0), Body: responseBody}, &metrics, nilLogger(), state, nil, "oz-transfer", recorder)
	require.NoError(t, recorder.Close())

	records := readTraceRecords(t, traceFile)
	require.Len(t, records, 1)
	require.Equal(t, "oz-transfer", records[0].Workload)
	require.Equal(t, "submit_response", records[0].Event)
	require.Equal(t, int64(9), records[0].RPCID)
	require.Equal(t, "PENDING", records[0].TransactionStatus)
	require.Equal(t, "abc123", records[0].Hash)
	require.Contains(t, records[0].ResponseBody, "PENDING")
}

func TestPollTransactionWithTraceWritesPollRequestAndResponse(t *testing.T) {
	traceFile := t.TempDir() + "/bench-trace.ndjson"
	recorder, err := openBenchmarkTraceRecorder(traceFile)
	require.NoError(t, err)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		require.Equal(t, protocol.GetTransactionMethodName, req["method"])
		response := rpcTraceEnvelope{
			JSONRPC: "2.0",
			ID:      req["id"],
			Result:  protocol.GetTransactionResponse{TransactionDetails: protocol.TransactionDetails{Status: protocol.TransactionStatusSuccess}},
		}
		require.NoError(t, json.NewEncoder(w).Encode(response))
	}))
	defer server.Close()

	rpc := rpcclient.NewClient(server.URL, nil)
	client := newSDKTransactionPollClient(rpc)
	recordPollRequestTrace("simple-payment", "feedbeef", 7, 1, recorder)
	results, err := client.GetTransactions(context.Background(), []transactionPollRequest{{ID: 1, Hash: "feedbeef"}})
	require.NoError(t, err)
	require.Len(t, results, 1)
	recordPollResponseTrace("simple-payment", "feedbeef", 7, 1, &results[0].Response, results[0].Err, recorder)
	require.NoError(t, recorder.Close())

	records := readTraceRecords(t, traceFile)
	require.Len(t, records, 2)
	require.Equal(t, "poll_request", records[0].Event)
	require.Equal(t, "poll_response", records[1].Event)
	require.Equal(t, int64(7), records[0].RPCID)
	require.Equal(t, "feedbeef", records[0].Hash)
	require.Equal(t, protocol.TransactionStatusSuccess, records[1].TransactionStatus)
}
