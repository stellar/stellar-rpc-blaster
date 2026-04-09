package benchmark

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stellar/go-stellar-sdk/keypair"
	protocol "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/txnbuild"
	"github.com/stellar/stellar-rpc-blaster/cmd/tx-load-test/state"
	"github.com/stretchr/testify/require"
	vegeta "github.com/tsenart/vegeta/v12/lib"
)

func TestSimplePaymentTargeterBuildsSinglePaymentOperation(t *testing.T) {
	source := keypair.MustRandom()
	recipientA := keypair.MustRandom()
	recipientB := keypair.MustRandom()
	feePayer := keypair.MustRandom()
	leases := &fakeLeaseManager{eligibleAny: []*keypair.Full{source}, eligibleTrustlined: []*keypair.Full{source}}

	targeter, err := newSimplePaymentTargeter(
		context.Background(),
		"https://rpc.example",
		&state.State{NetworkPassphrase: "Test SDF Network ; September 2015", FeePayerKP: feePayer},
		leases,
		[]*keypair.Full{source, recipientA, recipientB},
	)
	require.NoError(t, err)

	var target vegeta.Target
	require.NoError(t, targeter(&target))

	var request rpcJSONBody
	require.NoError(t, json.Unmarshal(target.Body, &request))
	require.Equal(t, protocol.SendTransactionMethodName, request.Method)

	gtx, err := txnbuild.TransactionFromXDR(request.Params["transaction"])
	require.NoError(t, err)
	feeBump, ok := gtx.FeeBump()
	require.True(t, ok)
	inner := feeBump.InnerTransaction()
	operations := inner.Operations()
	require.Len(t, operations, 1)
	payment, ok := operations[0].(*txnbuild.Payment)
	require.True(t, ok)
	require.Equal(t, simplePaymentAmount, payment.Amount)
	require.Equal(t, source.Address(), inner.SourceAccount().AccountID)
	require.Equal(t, int64(1), leases.RequestID())
	require.Empty(t, leases.retryableReleases)
	require.Empty(t, leases.ambiguousReleases)
	_, _ = recipientA, recipientB
}
